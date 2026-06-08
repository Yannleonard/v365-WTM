package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/hvconnect"
	"github.com/gtek-it/castor/server/internal/store"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// hvConnView is the API representation of a hypervisor connection (NEVER includes
// the credential; only hasSecret).
type hvConnView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	HasSecret   bool   `json:"hasSecret"`
	InsecureTLS bool   `json:"insecureTls"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"`
	LastError   string `json:"lastError,omitempty"`
	LastSeenAt  int64  `json:"lastSeenAt,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
}

func toHVConnView(h *store.HypervisorConn) hvConnView {
	return hvConnView{
		ID: h.ID, Name: h.Name, Kind: h.Kind, Endpoint: h.Endpoint, Username: h.Username,
		HasSecret: h.HasSecret(), InsecureTLS: h.InsecureTLS, Enabled: h.Enabled,
		Status: h.Status, LastError: h.LastError, LastSeenAt: h.LastSeenAt, CreatedAt: h.CreatedAt,
	}
}

var validHVKinds = map[string]bool{
	string(vprovider.KindKVM): true, string(vprovider.KindHyperV): true,
	string(vprovider.KindVMware): true, string(vprovider.KindXen): true,
}

// hvConnInput is the create/test request body.
type hvConnInput struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	Secret      string `json:"secret"`
	InsecureTLS bool   `json:"insecureTls"`
	Enabled     bool   `json:"enabled"`
}

func (in hvConnInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if !validHVKinds[in.Kind] {
		return authz.Errorf(authz.ErrValidation, "kind must be one of kvm|hyperv|vmware|xen")
	}
	// vmware/xen require an endpoint + credentials; kvm endpoint optional (local
	// socket default); hyperv endpoint optional ("" = local).
	if (in.Kind == string(vprovider.KindVMware) || in.Kind == string(vprovider.KindXen)) && strings.TrimSpace(in.Endpoint) == "" {
		return authz.Errorf(authz.ErrValidation, "endpoint is required for "+in.Kind)
	}
	return nil
}

// toConn opens nothing (plaintext secret straight from the input) for Verify/Build.
func (in hvConnInput) toConn(id string) hvconnect.Conn {
	return hvconnect.Conn{ID: id, Name: in.Name, Kind: in.Kind, Endpoint: in.Endpoint,
		Username: in.Username, Password: in.Secret, InsecureTLS: in.InsecureTLS}
}

// ListHypervisorConns returns all registered hypervisor connections (no secrets).
func (s *Server) ListHypervisorConns(w http.ResponseWriter, r *http.Request) {
	conns, err := s.store.ListHypervisorConns(r.Context())
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]hvConnView, 0, len(conns))
	for _, c := range conns {
		out = append(out, toHVConnView(c))
	}
	ok(w, out)
}

// TestHypervisorConn builds the live provider and health-checks it WITHOUT
// persisting — the "test connection" button before save.
func (s *Server) TestHypervisorConn(w http.ResponseWriter, r *http.Request) {
	var in hvConnInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := hvconnect.Verify(ctx, in.toConn("test-"+store.NewUUID())); err != nil {
		// A connection failure is a 422 (the input/endpoint is the problem), with a
		// sanitized message (no secrets are ever in these errors).
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "connection test failed: "+err.Error()))
		return
	}
	ok(w, map[string]any{"ok": true})
}

// CreateHypervisorConn persists a connection (sealing the secret) and, if enabled,
// connects + registers the LIVE provider immediately.
func (s *Server) CreateHypervisorConn(w http.ResponseWriter, r *http.Request) {
	var in hvConnInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	id := store.NewUUID()
	rec := &store.HypervisorConn{
		ID: id, Name: in.Name, Kind: in.Kind, Endpoint: in.Endpoint, Username: in.Username,
		InsecureTLS: in.InsecureTLS, Enabled: in.Enabled, Status: "pending",
	}
	if in.Secret != "" {
		sealed, err := authz.SealSecret(s.cfg.SecretKey, []byte(in.Secret))
		if err != nil {
			authz.WriteError(w, r, err)
			return
		}
		rec.SecretEnc = sealed
	}
	if err := s.store.CreateHypervisorConn(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	// Connect + register now if enabled (best-effort; status reflects the result).
	if rec.Enabled {
		s.connectHypervisor(r.Context(), rec, in.Secret)
		if updated, err := s.store.GetHypervisorConn(r.Context(), id); err == nil {
			rec = updated
		}
	}
	created(w, toHVConnView(rec))
}

// DeleteHypervisorConn deregisters the live provider and removes the connection.
func (s *Server) DeleteHypervisorConn(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if p, ok := s.vreg.Deregister(id); ok {
		_ = p.Close()
	}
	if err := s.store.DeleteHypervisorConn(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	noContent(w)
}

// connectHypervisor opens the sealed secret (or uses the provided plaintext on
// create), builds the LIVE provider, registers it, and records status. Never logs
// the secret. Used on create and on startup.
func (s *Server) connectHypervisor(ctx context.Context, rec *store.HypervisorConn, plaintextSecret string) {
	pass := plaintextSecret
	if pass == "" && len(rec.SecretEnc) > 0 {
		if opened, err := authz.OpenSecret(s.cfg.SecretKey, rec.SecretEnc); err == nil {
			pass = string(opened)
		}
	}
	conn := hvconnect.Conn{ID: rec.ID, Name: rec.Name, Kind: rec.Kind, Endpoint: rec.Endpoint,
		Username: rec.Username, Password: pass, InsecureTLS: rec.InsecureTLS}
	p, err := hvconnect.Build(conn)
	if err != nil {
		_ = s.store.UpdateHypervisorConnStatus(ctx, rec.ID, "error", err.Error())
		return
	}
	if _, err := p.HealthCheck(ctx); err != nil {
		_ = p.Close()
		_ = s.store.UpdateHypervisorConnStatus(ctx, rec.ID, "error", err.Error())
		return
	}
	s.vreg.Register(p)
	_ = s.store.UpdateHypervisorConnStatus(ctx, rec.ID, "connected", "")
}

// LoadHypervisorConnections connects + registers all enabled persisted connections.
// Called once at startup. Best-effort: a failing connection is recorded as 'error'
// and skipped, never blocking boot.
func (s *Server) LoadHypervisorConnections(ctx context.Context) {
	conns, err := s.store.ListHypervisorConns(ctx)
	if err != nil {
		return
	}
	for _, c := range conns {
		if c.Enabled {
			s.connectHypervisor(ctx, c, "")
		}
	}
}

// mountHypervisorConnRoutes wires the connection-management surface. Reads use
// vm.read; create/test/delete are admin-grade (vm.create) since they configure
// infrastructure access and handle credentials. Fixed mutation chain.
func (s *Server) mountHypervisorConnRoutes(pr chi.Router) {
	az := s.authz
	pr.With(az.RequirePermission("vm.read", nil)).Get("/vm/connections", s.ListHypervisorConns)
	pr.With(az.AuditWrap("vm.connection.test"), az.RequireAAL, az.RequirePermission("vm.create", nil)).
		Post("/vm/connections/test", s.TestHypervisorConn)
	pr.With(az.AuditWrap("vm.connection.create"), az.RequireAAL, az.RequirePermission("vm.create", nil)).
		Post("/vm/connections", s.CreateHypervisorConn)
	pr.With(az.AuditWrap("vm.connection.delete"), az.RequireAAL, az.RequirePermission("vm.create", nil)).
		Delete("/vm/connections/{id}", s.DeleteHypervisorConn)
}
