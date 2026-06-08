package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/migrate"
)

// migrateRequestBody is the V2V migration request payload.
type migrateRequestBody struct {
	SourceProviderID string `json:"sourceProviderId"`
	SourceVMID       string `json:"sourceVmId"`
	TargetProviderID string `json:"targetProviderId"`
	TargetHostID     string `json:"targetHostId"`
	TargetStorageID  string `json:"targetStorageId"`
	TargetName       string `json:"targetName"`
	PowerOnAfter     bool   `json:"powerOnAfter"`
}

func (b migrateRequestBody) toRequest() migrate.Request {
	return migrate.Request{
		SourceProviderID: b.SourceProviderID, SourceVMID: b.SourceVMID,
		TargetProviderID: b.TargetProviderID, TargetHostID: b.TargetHostID,
		TargetStorageID: b.TargetStorageID, TargetName: b.TargetName, PowerOnAfter: b.PowerOnAfter,
	}
}

// V2VPreflight runs the read-only pre-flight checks for a proposed migration.
func (s *Server) V2VPreflight(w http.ResponseWriter, r *http.Request) {
	var body migrateRequestBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	res, err := s.migEng.Preflight(ctx, body.toRequest())
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	ok(w, res)
}

// V2VStart launches a migration asynchronously and returns the job id.
func (s *Server) V2VStart(w http.ResponseWriter, r *http.Request) {
	var body migrateRequestBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	id := s.migEng.Start(body.toRequest())
	created(w, map[string]string{"id": id})
}

// V2VJobs lists all migration jobs.
func (s *Server) V2VJobs(w http.ResponseWriter, r *http.Request) {
	ok(w, s.migEng.List())
}

// V2VJob returns one migration job's progress.
func (s *Server) V2VJob(w http.ResponseWriter, r *http.Request) {
	p, found := s.migEng.Get(chi.URLParam(r, "jobID"))
	if !found {
		authz.WriteError(w, r, authz.ErrNotFound)
		return
	}
	ok(w, p)
}

// mountMigrateRoutes wires the V2V migration surface. Pre-flight + reads use
// v2v.read; starting a migration is an operator-grade write (v2v.migrate) gated by
// the fixed AuditWrap->AAL->RequirePermission chain. Global-scoped because a V2V
// spans two providers.
func (s *Server) mountMigrateRoutes(pr chi.Router) {
	az := s.authz
	pr.With(az.RequirePermission("v2v.read", nil)).Get("/v2v/jobs", s.V2VJobs)
	pr.With(az.RequirePermission("v2v.read", nil)).Get("/v2v/jobs/{jobID}", s.V2VJob)
	pr.With(az.RequirePermission("v2v.read", nil)).Post("/v2v/preflight", s.V2VPreflight)
	pr.With(az.AuditWrap("v2v.migrate"), az.RequireAAL, az.RequirePermission("v2v.migrate", nil)).
		Post("/v2v/migrate", s.V2VStart)
}
