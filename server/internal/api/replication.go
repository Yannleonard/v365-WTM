package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/replication"
	"github.com/gtek-it/castor/server/internal/store"
)

// replPolicyInput is the create-policy request body.
type replPolicyInput struct {
	Name             string `json:"name"`
	SourceProviderID string `json:"sourceProviderId"`
	SourceVMID       string `json:"sourceVmId"`
	TargetProviderID string `json:"targetProviderId"`
	TargetHostID     string `json:"targetHostId"`
	IntervalSeconds  int    `json:"intervalSeconds"`
	Retain           int    `json:"retain"`
	Enabled          bool   `json:"enabled"`
}

func (in replPolicyInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if strings.TrimSpace(in.SourceProviderID) == "" || strings.TrimSpace(in.SourceVMID) == "" {
		return authz.Errorf(authz.ErrValidation, "sourceProviderId and sourceVmId are required")
	}
	if strings.TrimSpace(in.TargetProviderID) == "" {
		return authz.Errorf(authz.ErrValidation, "targetProviderId is required")
	}
	if in.SourceProviderID == in.TargetProviderID {
		return authz.Errorf(authz.ErrValidation, "source and target providers must differ (cross-hypervisor DR)")
	}
	if in.IntervalSeconds < 0 || in.Retain < 0 {
		return authz.Errorf(authz.ErrValidation, "intervalSeconds and retain must be non-negative")
	}
	return nil
}

// replPolicyView merges the durable policy row with the engine's live DR state.
type replPolicyView struct {
	store.ReplicationPolicy
	State *replication.State `json:"state,omitempty"`
}

func (s *Server) toReplView(p *store.ReplicationPolicy) replPolicyView {
	v := replPolicyView{ReplicationPolicy: *p}
	if st, ok := s.replEng.State(p.ID); ok {
		v.State = st
	}
	return v
}

// toEnginePolicy maps a store row to the engine's Policy.
func toEnginePolicy(p *store.ReplicationPolicy) replication.Policy {
	return replication.Policy{
		ID: p.ID, Name: p.Name,
		SourceProviderID: p.SourceProviderID, SourceVMID: p.SourceVMID,
		TargetProviderID: p.TargetProviderID, TargetHostID: p.TargetHostID,
		IntervalSeconds: p.IntervalSeconds, Retain: p.Retain, Enabled: p.Enabled,
	}
}

// ListReplicationPolicies returns all policies + their live DR state.
func (s *Server) ListReplicationPolicies(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListReplicationPolicies(r.Context())
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]replPolicyView, 0, len(rows))
	for _, p := range rows {
		out = append(out, s.toReplView(p))
	}
	ok(w, out)
}

// GetReplicationPolicy returns one policy with status + cycle history.
func (s *Server) GetReplicationPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetReplicationPolicy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ok(w, s.toReplView(p))
}

// CreateReplicationPolicy persists a policy and (if enabled) schedules it in the
// engine immediately.
func (s *Server) CreateReplicationPolicy(w http.ResponseWriter, r *http.Request) {
	var in replPolicyInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec := &store.ReplicationPolicy{
		ID: store.NewUUID(), Name: in.Name,
		SourceProviderID: in.SourceProviderID, SourceVMID: in.SourceVMID,
		TargetProviderID: in.TargetProviderID, TargetHostID: in.TargetHostID,
		IntervalSeconds: in.IntervalSeconds, Retain: in.Retain, Enabled: in.Enabled,
		Status: string(replication.StatusIdle),
	}
	if err := s.store.CreateReplicationPolicy(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	s.replEng.Upsert(toEnginePolicy(rec))
	created(w, s.toReplView(rec))
}

// DeleteReplicationPolicy removes a policy from the engine + store.
func (s *Server) DeleteReplicationPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.replEng.Remove(id)
	if err := s.store.DeleteReplicationPolicy(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	noContent(w)
}

// RunReplicationPolicy triggers an immediate replication cycle ("Run now").
func (s *Server) RunReplicationPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetReplicationPolicy(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if _, ok := s.replEng.State(id); !ok {
		// Engine lost the policy (e.g. after a restart with persistence) — re-register.
		if p, err := s.store.GetReplicationPolicy(r.Context(), id); err == nil {
			s.replEng.Upsert(toEnginePolicy(p))
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	cyc, err := s.replEng.RunNow(ctx, id)
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	ok(w, cyc)
}

// FailoverReplicationPolicy powers on the replica + marks the policy failed-over.
func (s *Server) FailoverReplicationPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetReplicationPolicy(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	st, err := s.replEng.Failover(ctx, id)
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	ok(w, st)
}

// LoadReplicationPolicies registers + starts all enabled persisted policies. Called
// once at startup AFTER hypervisor connections are loaded (so the source/target
// providers are registered). Best-effort, mirroring LoadHypervisorConnections.
func (s *Server) LoadReplicationPolicies(ctx context.Context) {
	s.replEng.Start()
	rows, err := s.store.ListReplicationPolicies(ctx)
	if err != nil {
		return
	}
	for _, p := range rows {
		// Do not auto-resume a failed-over policy.
		if p.Status == string(replication.StatusFailedOver) {
			continue
		}
		s.replEng.Upsert(toEnginePolicy(p))
	}
}

// mountReplicationRoutes wires the cross-hypervisor replication surface. Reads use
// replication.read; create/delete/run/failover are operator-grade writes
// (replication.write). Mutations follow the fixed chain AuditWrap (OUTERMOST) ->
// RequireAAL -> RequirePermission -> handler so a denied mutation still records
// exactly one audit row. Global-scoped because a policy spans two providers.
func (s *Server) mountReplicationRoutes(pr chi.Router) {
	az := s.authz
	pr.With(az.RequirePermission("replication.read", nil)).
		Get("/replication/policies", s.ListReplicationPolicies)
	pr.With(az.RequirePermission("replication.read", nil)).
		Get("/replication/policies/{id}", s.GetReplicationPolicy)
	pr.With(az.AuditWrap("replication.policy.create"), az.RequireAAL, az.RequirePermission("replication.write", nil)).
		Post("/replication/policies", s.CreateReplicationPolicy)
	pr.With(az.AuditWrap("replication.policy.delete"), az.RequireAAL, az.RequirePermission("replication.write", nil)).
		Delete("/replication/policies/{id}", s.DeleteReplicationPolicy)
	pr.With(az.AuditWrap("replication.policy.run"), az.RequireAAL, az.RequirePermission("replication.write", nil)).
		Post("/replication/policies/{id}/run", s.RunReplicationPolicy)
	pr.With(az.AuditWrap("replication.policy.failover"), az.RequireAAL, az.RequirePermission("replication.write", nil)).
		Post("/replication/policies/{id}/failover", s.FailoverReplicationPolicy)
}
