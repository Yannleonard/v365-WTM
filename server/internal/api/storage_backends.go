package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/storage"
	"github.com/gtek-it/castor/server/internal/store"
)

// storageBackendView is the API representation of a storage backend. It NEVER
// includes the credential; only hasSecret. Mirrors hvConnView.
type storageBackendView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint"`
	Target     string `json:"target"`
	Username   string `json:"username"`
	HasSecret  bool   `json:"hasSecret"`
	Region     string `json:"region,omitempty"`
	ProviderID string `json:"providerId,omitempty"`
	Options    string `json:"options,omitempty"`
	Enabled    bool   `json:"enabled"`
	Status     string `json:"status"`
	LastError  string `json:"lastError,omitempty"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

func toStorageBackendView(b *store.StorageBackend) storageBackendView {
	return storageBackendView{
		ID: b.ID, Name: b.Name, Type: b.Type, Endpoint: b.Endpoint, Target: b.Target,
		Username: b.Username, HasSecret: b.HasSecret(), Region: b.Region, ProviderID: b.ProviderID,
		Options: b.Options, Enabled: b.Enabled, Status: b.Status, LastError: b.LastError,
		LastSeenAt: b.LastSeenAt, CreatedAt: b.CreatedAt,
	}
}

// storageBackendInput is the create/test request body. `secret` is the plaintext
// credential sent once; it is sealed server-side and never returned.
type storageBackendInput struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint"`
	Target     string `json:"target"`
	Username   string `json:"username"`
	Secret     string `json:"secret"`
	Region     string `json:"region"`
	ProviderID string `json:"providerId"`
	Options    string `json:"options"`
	Enabled    bool   `json:"enabled"`
}

func (in storageBackendInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if !storage.ValidType(in.Type) {
		return authz.Errorf(authz.ErrValidation, "type must be one of nfs|iscsi|smb|azureblob|s3")
	}
	cfg := storage.Config{
		Type: storage.Type(in.Type), Endpoint: in.Endpoint, Target: in.Target,
		Username: in.Username, Secret: in.Secret, Region: in.Region,
	}
	if err := cfg.Validate(); err != nil {
		return authz.Errorf(authz.ErrValidation, err.Error())
	}
	return nil
}

// toConfig builds a storage.Config from the input, resolving the libvirt endpoint
// for the SAN/NAS family from the target KVM provider (so the pool is defined on
// the right libvirt host).
func (s *Server) toConfig(in storageBackendInput) storage.Config {
	cfg := storage.Config{
		Type:       storage.Type(in.Type),
		Name:       in.Name,
		Endpoint:   in.Endpoint,
		Target:     in.Target,
		Username:   in.Username,
		Secret:     in.Secret,
		Region:     in.Region,
		ProviderID: in.ProviderID,
	}
	if storage.IsSAN(cfg.Type) {
		cfg.LibvirtEndpoint = s.libvirtEndpointForProvider(in.ProviderID)
	}
	return cfg
}

// libvirtEndpointForProvider resolves the libvirt RPC endpoint of a registered KVM
// provider so the SAN/NAS pool is defined on that host. When no provider is given
// or it is not a libvirt endpoint provider, the empty string falls back to the
// local libvirt socket (the host UniHV itself runs on).
func (s *Server) libvirtEndpointForProvider(providerID string) string {
	if providerID == "" {
		return ""
	}
	if p, ok := s.vreg.Get(providerID); ok {
		if le, ok := p.(interface{ LibvirtEndpoint() string }); ok {
			return le.LibvirtEndpoint()
		}
	}
	return ""
}

// ListStorageBackends returns all registered storage backends (no secrets).
func (s *Server) ListStorageBackends(w http.ResponseWriter, r *http.Request) {
	backends, err := s.store.ListStorageBackends(r.Context())
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]storageBackendView, 0, len(backends))
	for _, b := range backends {
		out = append(out, toStorageBackendView(b))
	}
	ok(w, out)
}

// TestStorageBackend builds the backend and verifies connectivity WITHOUT
// persisting — the "test" button before save.
func (s *Server) TestStorageBackend(w http.ResponseWriter, r *http.Request) {
	var in storageBackendInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	be, err := storage.New(s.toConfig(in))
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	if err := be.Test(ctx); err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "storage test failed: "+err.Error()))
		return
	}
	ok(w, map[string]any{"ok": true})
}

// CreateStorageBackend persists a backend (sealing the secret) and best-effort
// probes it to record an initial status.
func (s *Server) CreateStorageBackend(w http.ResponseWriter, r *http.Request) {
	var in storageBackendInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	id := store.NewUUID()
	rec := &store.StorageBackend{
		ID: id, Name: in.Name, Type: in.Type, Endpoint: in.Endpoint, Target: in.Target,
		Username: in.Username, Region: in.Region, ProviderID: in.ProviderID, Options: in.Options,
		Enabled: in.Enabled, Status: "pending",
	}
	if in.Secret != "" {
		sealed, err := authz.SealSecret(s.cfg.SecretKey, []byte(in.Secret))
		if err != nil {
			authz.WriteError(w, r, err)
			return
		}
		rec.SecretEnc = sealed
	}
	if err := s.store.CreateStorageBackend(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	// Best-effort connectivity probe if enabled; status reflects the result.
	if rec.Enabled {
		s.probeStorageBackend(r.Context(), rec, in.Secret)
		if updated, err := s.store.GetStorageBackend(r.Context(), id); err == nil {
			rec = updated
		}
	}
	created(w, toStorageBackendView(rec))
}

// DeleteStorageBackend removes a backend by id.
func (s *Server) DeleteStorageBackend(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteStorageBackend(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	noContent(w)
}

// probeStorageBackend opens the sealed secret (or uses the provided plaintext on
// create), builds the backend, tests it, and records status. Never logs the secret.
func (s *Server) probeStorageBackend(ctx context.Context, rec *store.StorageBackend, plaintextSecret string) {
	secret := plaintextSecret
	if secret == "" && len(rec.SecretEnc) > 0 {
		if opened, err := authz.OpenSecret(s.cfg.SecretKey, rec.SecretEnc); err == nil {
			secret = string(opened)
		}
	}
	cfg := storage.Config{
		Type: storage.Type(rec.Type), Name: rec.Name, Endpoint: rec.Endpoint, Target: rec.Target,
		Username: rec.Username, Secret: secret, Region: rec.Region, ProviderID: rec.ProviderID,
	}
	if storage.IsSAN(cfg.Type) {
		cfg.LibvirtEndpoint = s.libvirtEndpointForProvider(rec.ProviderID)
	}
	be, err := storage.New(cfg)
	if err != nil {
		_ = s.store.UpdateStorageBackendStatus(ctx, rec.ID, "error", err.Error())
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := be.Test(pctx); err != nil {
		_ = s.store.UpdateStorageBackendStatus(ctx, rec.ID, "error", err.Error())
		return
	}
	_ = s.store.UpdateStorageBackendStatus(ctx, rec.ID, "connected", "")
}

// mountStorageBackendRoutes wires the storage-backend management surface. Reads
// require storage.backend.read; create/test/delete require storage.backend.write
// (admin-grade — they configure infrastructure access and handle credentials).
func (s *Server) mountStorageBackendRoutes(pr chi.Router) {
	az := s.authz
	pr.With(az.RequirePermission("storage.backend.read", nil)).Get("/storage/backends", s.ListStorageBackends)
	pr.With(az.AuditWrap("storage.backend.test"), az.RequireAAL, az.RequirePermission("storage.backend.write", nil)).
		Post("/storage/backends/test", s.TestStorageBackend)
	pr.With(az.AuditWrap("storage.backend.create"), az.RequireAAL, az.RequirePermission("storage.backend.write", nil)).
		Post("/storage/backends", s.CreateStorageBackend)
	pr.With(az.AuditWrap("storage.backend.delete"), az.RequireAAL, az.RequirePermission("storage.backend.write", nil)).
		Delete("/storage/backends/{id}", s.DeleteStorageBackend)
}
