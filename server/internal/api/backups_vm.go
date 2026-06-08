package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	bk "github.com/gtek-it/castor/server/internal/backup"
	"github.com/gtek-it/castor/server/internal/storage"
	"github.com/gtek-it/castor/server/internal/store"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// backupBackendResolver adapts the storage-backend store + sealed-secret opening
// into the backup engine's BackendResolver: it loads a storage_backends row,
// opens the credential, builds storage.New(...), and returns its ObjectStore.
type backupBackendResolver struct{ s *Server }

func (r backupBackendResolver) ObjectStore(ctx context.Context, backendID string) (storage.ObjectStore, error) {
	rec, err := r.s.store.GetStorageBackend(ctx, backendID)
	if err != nil {
		return nil, err
	}
	secret := ""
	if len(rec.SecretEnc) > 0 {
		if opened, oerr := authz.OpenSecret(r.s.cfg.SecretKey, rec.SecretEnc); oerr == nil {
			secret = string(opened)
		}
	}
	cfg := storage.Config{
		Type: storage.Type(rec.Type), Name: rec.Name, Endpoint: rec.Endpoint, Target: rec.Target,
		Username: rec.Username, Secret: secret, Region: rec.Region, ProviderID: rec.ProviderID,
	}
	if storage.IsSAN(cfg.Type) {
		cfg.LibvirtEndpoint = r.s.libvirtEndpointForProvider(rec.ProviderID)
	}
	be, err := storage.New(cfg)
	if err != nil {
		return nil, err
	}
	os, ok := storage.AsObjectStore(be)
	if !ok {
		return nil, authz.Errorf(authz.ErrValidation, "storage backend type "+rec.Type+" cannot store backup artifacts")
	}
	return os, nil
}

// backupRecorder adapts *store.Store to the backup engine's Recorder, translating
// between the engine's Record and the store's VMBackup row.
type backupRecorder struct{ s *Server }

func (r backupRecorder) RecordBackup(ctx context.Context, b *bk.Record) error {
	disksJSON, _ := json.Marshal(b.Disks)
	return r.s.store.CreateVMBackup(ctx, &store.VMBackup{
		ID: b.ID, VMID: b.VMID, VMName: b.VMName, ProviderID: b.ProviderID,
		BackendID: b.BackendID, PolicyID: b.PolicyID, KeyPrefix: b.KeyPrefix,
		SizeBytes: b.SizeBytes, DiskCount: b.DiskCount, DisksJSON: string(disksJSON),
		GuestOS: b.GuestOS, Firmware: string(b.Firmware), Status: b.Status, CreatedAt: b.CreatedAt,
	})
}

func (r backupRecorder) UpdateBackupResult(ctx context.Context, id, status string, sizeBytes int64, diskCount int, disksJSON, errMsg string) error {
	return r.s.store.UpdateVMBackupResult(ctx, id, status, sizeBytes, diskCount, disksJSON, errMsg)
}

func (r backupRecorder) GetBackupRecord(ctx context.Context, id string) (*bk.Record, error) {
	row, err := r.s.store.GetVMBackup(ctx, id)
	if err != nil {
		return nil, err
	}
	return storeBackupToRecord(row), nil
}

func (r backupRecorder) ListPolicyBackups(ctx context.Context, policyID string) ([]*bk.Record, error) {
	rows, err := r.s.store.ListVMBackupsForPolicy(ctx, policyID)
	if err != nil {
		return nil, err
	}
	out := make([]*bk.Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, storeBackupToRecord(row))
	}
	return out, nil
}

func (r backupRecorder) DeleteBackupRow(ctx context.Context, id string) error {
	return r.s.store.DeleteVMBackup(ctx, id)
}

func (r backupRecorder) UpdatePolicyState(ctx context.Context, id, status, lastErr string, lastRunAt int64) error {
	return r.s.store.UpdateVMBackupPolicyState(ctx, id, status, lastErr, lastRunAt)
}

// storeBackupToRecord maps a persisted VMBackup row to the engine's Record.
func storeBackupToRecord(row *store.VMBackup) *bk.Record {
	var disks []bk.DiskArtifact
	if row.DisksJSON != "" {
		_ = json.Unmarshal([]byte(row.DisksJSON), &disks)
	}
	return &bk.Record{
		ID: row.ID, VMID: row.VMID, VMName: row.VMName, ProviderID: row.ProviderID,
		BackendID: row.BackendID, PolicyID: row.PolicyID, KeyPrefix: row.KeyPrefix,
		SizeBytes: row.SizeBytes, DiskCount: row.DiskCount, Disks: disks,
		GuestOS: row.GuestOS, Firmware: vp.Firmware(row.Firmware), Status: row.Status,
		Error: row.Error, CreatedAt: row.CreatedAt,
	}
}

/* ============================== handlers ============================== */

// runBackupInput is the POST /vm-backups/run body.
type runBackupInput struct {
	ProviderID string `json:"providerId"`
	VMID       string `json:"vmId"`
	BackendID  string `json:"backendId"`
}

// RunVMBackup performs an immediate ad-hoc backup (the "Back up now" action).
func (s *Server) RunVMBackup(w http.ResponseWriter, r *http.Request) {
	var in runBackupInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(in.ProviderID) == "" || strings.TrimSpace(in.VMID) == "" || strings.TrimSpace(in.BackendID) == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "providerId, vmId and backendId are required"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	rec, err := s.backupEng.BackupNow(ctx, bk.BackupRequest{
		ProviderID: in.ProviderID, VMID: in.VMID, BackendID: in.BackendID,
	})
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	created(w, rec)
}

// ListVMBackups returns backups (optionally filtered by ?vmId=).
func (s *Server) ListVMBackups(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListVMBackups(r.Context(), r.URL.Query().Get("vmId"))
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]*bk.Record, 0, len(rows))
	for _, row := range rows {
		out = append(out, storeBackupToRecord(row))
	}
	ok(w, out)
}

// DeleteVMBackup removes a backup's artifacts + row.
func (s *Server) DeleteVMBackup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetVMBackup(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := s.backupEng.DeleteBackup(ctx, id); err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	noContent(w)
}

// restoreInput is the POST /vm-backups/{id}/restore body.
type restoreInput struct {
	TargetProviderID string `json:"targetProviderId"`
	TargetHostID     string `json:"targetHostId"`
	TargetName       string `json:"targetName"`
	PowerOn          bool   `json:"powerOn"`
}

// RestoreVMBackup imports a backup as a NEW VM on the target provider.
func (s *Server) RestoreVMBackup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in restoreInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(in.TargetProviderID) == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "targetProviderId is required"))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	newVMID, err := s.backupEng.Restore(ctx, bk.RestoreRequest{
		BackupID: id, TargetProviderID: in.TargetProviderID, TargetHostID: in.TargetHostID,
		TargetName: in.TargetName, PowerOnAfter: in.PowerOn,
	})
	if err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	ok(w, map[string]any{"vmId": newVMID})
}

/* --------------------------- backup policies --------------------------- */

// backupPolicyInput is the create-policy body.
type backupPolicyInput struct {
	Name            string `json:"name"`
	ProviderID      string `json:"providerId"`
	VMID            string `json:"vmId"`
	BackendID       string `json:"backendId"`
	IntervalSeconds int    `json:"intervalSeconds"`
	RetentionCount  int    `json:"retentionCount"`
	Enabled         bool   `json:"enabled"`
}

func (in backupPolicyInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if strings.TrimSpace(in.ProviderID) == "" || strings.TrimSpace(in.VMID) == "" {
		return authz.Errorf(authz.ErrValidation, "providerId and vmId are required")
	}
	if strings.TrimSpace(in.BackendID) == "" {
		return authz.Errorf(authz.ErrValidation, "backendId is required")
	}
	if in.IntervalSeconds < 0 || in.RetentionCount < 0 {
		return authz.Errorf(authz.ErrValidation, "intervalSeconds and retentionCount must be non-negative")
	}
	return nil
}

func toBackupEnginePolicy(p *store.VMBackupPolicy) bk.Policy {
	return bk.Policy{
		ID: p.ID, Name: p.Name, ProviderID: p.ProviderID, VMID: p.VMID,
		BackendID: p.BackendID, IntervalSeconds: p.IntervalSeconds,
		RetentionCount: p.RetentionCount, Enabled: p.Enabled,
	}
}

// ListVMBackupPolicies returns all scheduled-backup policies.
func (s *Server) ListVMBackupPolicies(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListVMBackupPolicies(r.Context())
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ok(w, rows)
}

// CreateVMBackupPolicy persists a policy and schedules it if enabled.
func (s *Server) CreateVMBackupPolicy(w http.ResponseWriter, r *http.Request) {
	var in backupPolicyInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec := &store.VMBackupPolicy{
		ID: store.NewUUID(), Name: in.Name, ProviderID: in.ProviderID, VMID: in.VMID,
		BackendID: in.BackendID, IntervalSeconds: in.IntervalSeconds,
		RetentionCount: in.RetentionCount, Enabled: in.Enabled, Status: "idle",
	}
	if err := s.store.CreateVMBackupPolicy(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	s.backupEng.Upsert(toBackupEnginePolicy(rec))
	created(w, rec)
}

// RunVMBackupPolicy triggers a policy's backup immediately (+ retention prune).
func (s *Server) RunVMBackupPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	p, err := s.store.GetVMBackupPolicy(r.Context(), id)
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	// Ensure the engine knows the policy (after a restart).
	s.backupEng.Upsert(toBackupEnginePolicy(p))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	if err := s.backupEng.RunPolicyNow(ctx, id); err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, err.Error()))
		return
	}
	ok(w, ActionResult{OK: true})
}

// DeleteVMBackupPolicy removes a policy from the engine + store.
func (s *Server) DeleteVMBackupPolicy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.backupEng.Remove(id)
	if err := s.store.DeleteVMBackupPolicy(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	noContent(w)
}

// LoadVMBackupPolicies starts the backup engine + resumes enabled policies. Called
// once at startup AFTER hypervisor connections load (so providers exist). Mirrors
// LoadReplicationPolicies.
func (s *Server) LoadVMBackupPolicies(ctx context.Context) {
	s.backupEng.Start()
	rows, err := s.store.ListVMBackupPolicies(ctx)
	if err != nil {
		return
	}
	for _, p := range rows {
		s.backupEng.Upsert(toBackupEnginePolicy(p))
	}
}

// mountVMBackupRoutes wires the scheduled-VM-backup surface. Reads use vm.backup;
// run/delete/policy-CRUD use vm.backup; restore is the most sensitive verb and
// uses vm.backup.restore (admin-grade). Mutations follow the fixed chain
// AuditWrap (OUTERMOST) -> RequireAAL -> RequirePermission -> handler so a denied
// mutation still records exactly one audit row. Global-scoped (a backup spans a
// provider + a storage backend).
func (s *Server) mountVMBackupRoutes(pr chi.Router) {
	az := s.authz

	pr.With(az.RequirePermission("vm.backup", nil)).Get("/vm-backups", s.ListVMBackups)
	pr.With(az.AuditWrap("vm.backup.run"), az.RequireAAL, az.RequirePermission("vm.backup", nil)).
		Post("/vm-backups/run", s.RunVMBackup)
	pr.With(az.AuditWrap("vm.backup.delete"), az.RequireAAL, az.RequirePermission("vm.backup", nil)).
		Delete("/vm-backups/{id}", s.DeleteVMBackup)
	pr.With(az.AuditWrap("vm.backup.restore"), az.RequireAAL, az.RequirePermission("vm.backup.restore", nil)).
		Post("/vm-backups/{id}/restore", s.RestoreVMBackup)

	pr.With(az.RequirePermission("vm.backup", nil)).Get("/vm-backup-policies", s.ListVMBackupPolicies)
	pr.With(az.AuditWrap("vm.backup.policy.create"), az.RequireAAL, az.RequirePermission("vm.backup", nil)).
		Post("/vm-backup-policies", s.CreateVMBackupPolicy)
	pr.With(az.AuditWrap("vm.backup.policy.run"), az.RequireAAL, az.RequirePermission("vm.backup", nil)).
		Post("/vm-backup-policies/{id}/run", s.RunVMBackupPolicy)
	pr.With(az.AuditWrap("vm.backup.policy.delete"), az.RequireAAL, az.RequirePermission("vm.backup", nil)).
		Delete("/vm-backup-policies/{id}", s.DeleteVMBackupPolicy)
}
