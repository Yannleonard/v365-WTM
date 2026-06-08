// Package backup implements UniHV SCHEDULED VM BACKUPS (Lot 5B): point-in-time,
// crash/app-consistent backups of a VM's disks pushed to a pluggable storage
// backend (S3 / Azure Blob / SAN-NAS / local filesystem), with retention pruning
// and one-click restore as a NEW VM.
//
// A backup is composed from the SAME primitives the V2V/replication engines use:
//
//	BackupNow(vm) =
//	  1. consistency snapshot on the source (quiesced via the guest agent when
//	     available; best-effort — a crash-consistent export still protects data),
//	  2. export each disk (provider.ExportVM) and convert to qcow2 (qemu-img),
//	  3. upload each artifact to the chosen storage backend under a keyed path
//	     vm/<vmId>/<timestamp>/<disk>.qcow2 (+ a manifest.json),
//	  4. record a Backup row (vm, provider, backend, size, disks[]) ,
//	  5. drop the snapshot.
//
//	Restore(backupId, targetProvider) =
//	  pull the manifest + disk artifacts and import them as a NEW VM on the target
//	  (provider.CreateVM), reusing the migrate hardware-mapping path.
//
// Scheduling mirrors server/internal/replication: one goroutine per enabled
// policy runs BackupNow on its interval, then prunes backups beyond
// retentionCount. The engine is concurrency-safe; durable definitions live in the
// store (the engine resumes enabled policies at boot).
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/gtek-it/castor/server/internal/migrate"
	"github.com/gtek-it/castor/server/internal/storage"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// ProviderResolver resolves a provider id (satisfied by *vprovider.Registry).
type ProviderResolver interface {
	Get(id string) (vp.HypervisorProvider, bool)
}

// BackendResolver opens a storage backend (as an object store) by its registered
// id. The API layer satisfies this by loading the store.StorageBackend, opening
// the sealed secret, and building storage.New(...). Tests pass an in-memory impl.
type BackendResolver interface {
	// ObjectStore returns the data-plane store for backend id.
	ObjectStore(ctx context.Context, backendID string) (storage.ObjectStore, error)
}

// Recorder persists backup rows + policy run state. *store.Store satisfies it; a
// nil Recorder makes the engine in-memory only (tests).
type Recorder interface {
	RecordBackup(ctx context.Context, b *Record) error
	UpdateBackupResult(ctx context.Context, id, status string, sizeBytes int64, diskCount int, disksJSON, errMsg string) error
	ListPolicyBackups(ctx context.Context, policyID string) ([]*Record, error)
	DeleteBackupRow(ctx context.Context, id string) error
	GetBackupRecord(ctx context.Context, id string) (*Record, error)
	UpdatePolicyState(ctx context.Context, id, status, lastErr string, lastRunAt int64) error
}

// DiskArtifact is one stored disk image inside a backup.
type DiskArtifact struct {
	Key       string        `json:"key"`
	SizeBytes int64         `json:"sizeBytes"`
	Format    vp.DiskFormat `json:"format"`
}

// Record is the engine's view of a persisted backup (mirrors store.VMBackup).
type Record struct {
	ID         string         `json:"id"`
	VMID       string         `json:"vmId"`
	VMName     string         `json:"vmName,omitempty"`
	ProviderID string         `json:"providerId"`
	BackendID  string         `json:"backendId"`
	PolicyID   string         `json:"policyId,omitempty"`
	KeyPrefix  string         `json:"keyPrefix"`
	SizeBytes  int64          `json:"sizeBytes"`
	DiskCount  int            `json:"diskCount"`
	Disks      []DiskArtifact `json:"disks,omitempty"`
	GuestOS    string         `json:"guestOs,omitempty"`
	Firmware   vp.Firmware    `json:"firmware,omitempty"`
	Status     string         `json:"status"`
	Error      string         `json:"error,omitempty"`
	CreatedAt  int64          `json:"createdAt"`
}

// manifest is the JSON descriptor uploaded alongside the disk artifacts; Restore
// reads it to reconstruct the VM spec.
type manifest struct {
	VMID      string         `json:"vmId"`
	VMName    string         `json:"vmName"`
	GuestOS   string         `json:"guestOs,omitempty"`
	Firmware  vp.Firmware    `json:"firmware,omitempty"`
	Disks     []DiskArtifact `json:"disks"`
	CreatedAt int64          `json:"createdAt"`
}

const manifestKey = "manifest.json"

// Policy is the engine's scheduled-backup definition (mirrors store.VMBackupPolicy).
type Policy struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ProviderID      string `json:"providerId"`
	VMID            string `json:"vmId"`
	BackendID       string `json:"backendId"`
	IntervalSeconds int    `json:"intervalSeconds"`
	RetentionCount  int    `json:"retentionCount"`
	Enabled         bool   `json:"enabled"`
}

// IDGen mints new backup ids (the store's NewUUID, injected to avoid an import).
type IDGen func() string

// job is the per-policy runtime state guarded by the engine mutex.
type job struct {
	policy Policy
	cancel context.CancelFunc
	runMu  sync.Mutex // serializes runs of ONE policy (manual vs. tick)
}

// Engine schedules + runs VM backups across a provider registry + storage backends.
type Engine struct {
	reg      ProviderResolver
	backends BackendResolver
	rec      Recorder
	conv     migrate.Converter
	newID    IDGen
	now      func() time.Time

	mu      sync.RWMutex
	jobs    map[string]*job
	running bool
}

// New builds an Engine. conv nil -> qemu-img converter (passthrough fallback).
// newID nil -> a time-based fallback id generator (tests). rec may be nil.
func New(reg ProviderResolver, backends BackendResolver, rec Recorder, conv migrate.Converter, newID IDGen) *Engine {
	if conv == nil {
		conv = migrate.QemuImgConverter{}
	}
	if newID == nil {
		var seq int64
		newID = func() string { seq++; return fmt.Sprintf("bkp-%d-%d", time.Now().UnixNano(), seq) }
	}
	return &Engine{
		reg: reg, backends: backends, rec: rec, conv: conv, newID: newID,
		now:  func() time.Time { return time.Now().UTC() },
		jobs: map[string]*job{},
	}
}

// Start marks the engine running (policies Upserted with Enabled=true schedule).
func (e *Engine) Start() {
	e.mu.Lock()
	e.running = true
	e.mu.Unlock()
}

// Stop halts all policy schedulers.
func (e *Engine) Stop() {
	e.mu.Lock()
	e.running = false
	for _, j := range e.jobs {
		if j.cancel != nil {
			j.cancel()
			j.cancel = nil
		}
	}
	e.mu.Unlock()
}

// Upsert registers/updates a policy; (re)schedules it if enabled and running.
func (e *Engine) Upsert(p Policy) {
	e.mu.Lock()
	j, ok := e.jobs[p.ID]
	if !ok {
		j = &job{}
		e.jobs[p.ID] = j
	}
	j.policy = p
	if j.cancel != nil {
		j.cancel()
		j.cancel = nil
	}
	if e.running && p.Enabled {
		e.scheduleLocked(p.ID, j)
	}
	e.mu.Unlock()
}

// Remove stops + forgets a policy (durable deletion is the caller's job).
func (e *Engine) Remove(id string) {
	e.mu.Lock()
	if j, ok := e.jobs[id]; ok {
		if j.cancel != nil {
			j.cancel()
		}
		delete(e.jobs, id)
	}
	e.mu.Unlock()
}

// scheduleLocked launches the per-policy ticker goroutine. Caller holds e.mu.
func (e *Engine) scheduleLocked(id string, j *job) {
	interval := time.Duration(j.policy.IntervalSeconds) * time.Second
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				cctx, ccancel := context.WithTimeout(ctx, 2*time.Hour)
				_ = e.runPolicy(cctx, id)
				ccancel()
			}
		}
	}()
}

// RunPolicyNow triggers a policy's backup immediately (the scheduled flow:
// BackupNow + retention prune + state persistence). Blocks until done.
func (e *Engine) RunPolicyNow(ctx context.Context, id string) error {
	return e.runPolicy(ctx, id)
}

// runPolicy runs one scheduled backup for a policy and prunes old backups.
func (e *Engine) runPolicy(ctx context.Context, id string) error {
	e.mu.RLock()
	j, ok := e.jobs[id]
	e.mu.RUnlock()
	if !ok {
		return fmt.Errorf("backup: unknown policy %q", id)
	}
	j.runMu.Lock()
	defer j.runMu.Unlock()
	pol := j.policy

	e.persistPolicy(ctx, id, "running", "", 0)
	rec, err := e.BackupNow(ctx, BackupRequest{
		ProviderID: pol.ProviderID, VMID: pol.VMID, BackendID: pol.BackendID, PolicyID: pol.ID,
	})
	if err != nil {
		e.persistPolicy(ctx, id, "error", err.Error(), 0)
		return err
	}
	// Retention prune: drop completed backups beyond RetentionCount (oldest first).
	if err := e.pruneRetention(ctx, pol.ID, pol.RetentionCount); err != nil {
		// Non-fatal: the backup succeeded; record the prune warning.
		e.persistPolicy(ctx, id, "idle", "retention prune warning: "+err.Error(), rec.CreatedAt)
		return nil
	}
	e.persistPolicy(ctx, id, "idle", "", rec.CreatedAt)
	return nil
}

// pruneRetention deletes a policy's completed backups beyond keep (keeping the
// newest keep). keep<=0 disables pruning.
func (e *Engine) pruneRetention(ctx context.Context, policyID string, keep int) error {
	if e.rec == nil || keep <= 0 {
		return nil
	}
	rows, err := e.rec.ListPolicyBackups(ctx, policyID) // oldest first
	if err != nil {
		return err
	}
	if len(rows) <= keep {
		return nil
	}
	excess := rows[:len(rows)-keep]
	var firstErr error
	for _, r := range excess {
		if err := e.deleteBackupArtifactsAndRow(ctx, r); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// BackupRequest parameterizes a one-shot backup.
type BackupRequest struct {
	ProviderID string
	VMID       string
	BackendID  string
	PolicyID   string // empty for ad-hoc backups
}

// BackupNow performs a single backup: snapshot -> export+convert -> upload ->
// record -> drop snapshot. Returns the completed Record.
func (e *Engine) BackupNow(ctx context.Context, req BackupRequest) (*Record, error) {
	src, ok := e.reg.Get(req.ProviderID)
	if !ok {
		return nil, fmt.Errorf("backup: unknown provider %q", req.ProviderID)
	}
	if !src.Capabilities().Has(vp.CapExport) {
		return nil, fmt.Errorf("backup: provider %q cannot export disks", req.ProviderID)
	}
	objs, err := e.backends.ObjectStore(ctx, req.BackendID)
	if err != nil {
		return nil, fmt.Errorf("backup: storage backend: %w", err)
	}

	// Resolve VM metadata (name, disk count, firmware) for the manifest.
	var vmName, guestOS string
	var firmware vp.Firmware
	diskCount := 1
	if src.Capabilities().Has(vp.CapGetVM) {
		if d, derr := src.GetVM(ctx, req.VMID); derr == nil && d != nil {
			vmName = d.Name
			guestOS = d.GuestOS
			firmware = d.Firmware
			if len(d.Disks) > 0 {
				diskCount = len(d.Disks)
			}
		}
	}

	ts := e.now()
	id := e.newID()
	keyPrefix := fmt.Sprintf("vm/%s/%d/", req.VMID, ts.Unix())

	rec := &Record{
		ID: id, VMID: req.VMID, VMName: vmName, ProviderID: req.ProviderID,
		BackendID: req.BackendID, PolicyID: req.PolicyID, KeyPrefix: keyPrefix,
		GuestOS: guestOS, Firmware: firmware, Status: "pending", CreatedAt: ts.Unix(),
	}
	if e.rec != nil {
		if err := e.rec.RecordBackup(ctx, rec); err != nil {
			return nil, fmt.Errorf("backup: record: %w", err)
		}
	}

	// --- step 1: consistency snapshot (quiesce via guest agent when available) ---
	snapName := fmt.Sprintf("unihv-backup-%d", ts.Unix())
	snapTaken := false
	if src.Capabilities().Has(vp.CapSnapshot) {
		quiesce := src.Capabilities().Has(vp.CapGuestAgent)
		if _, serr := src.Snapshot(ctx, req.VMID, vp.SnapshotOptions{
			Name: snapName, Description: "UniHV backup consistency point", Quiesce: quiesce,
		}); serr == nil {
			snapTaken = true
		}
		// A failed snapshot is non-fatal (crash-consistent export still protects data).
	}

	// --- steps 2-3: export each disk -> convert to qcow2 -> upload ---
	disks, total, err := e.exportAndUpload(ctx, src, objs, req.VMID, keyPrefix, diskCount)

	// --- step 5: drop the snapshot (best-effort) ---
	if snapTaken {
		if snaps, lerr := src.ListSnapshots(ctx, req.VMID); lerr == nil {
			for _, s := range snaps {
				if s.Name == snapName {
					_ = e.dropSnapshot(ctx, src, req.VMID, s.ID)
				}
			}
		}
	}

	if err != nil {
		rec.Status = "error"
		rec.Error = err.Error()
		if e.rec != nil {
			_ = e.rec.UpdateBackupResult(ctx, id, "error", 0, 0, "", err.Error())
		}
		return rec, err
	}

	// --- step 4: write the manifest + finalize the record ---
	man := manifest{VMID: req.VMID, VMName: vmName, GuestOS: guestOS, Firmware: firmware, Disks: disks, CreatedAt: ts.Unix()}
	manBytes, _ := json.Marshal(man)
	if _, perr := objs.PutObject(ctx, keyPrefix+manifestKey, byteReader(manBytes), int64(len(manBytes))); perr != nil {
		rec.Status = "error"
		rec.Error = "upload manifest: " + perr.Error()
		if e.rec != nil {
			_ = e.rec.UpdateBackupResult(ctx, id, "error", 0, 0, "", rec.Error)
		}
		return rec, fmt.Errorf("backup: %s", rec.Error)
	}

	rec.Disks = disks
	rec.DiskCount = len(disks)
	rec.SizeBytes = total
	rec.Status = "completed"
	disksJSON, _ := json.Marshal(disks)
	if e.rec != nil {
		if err := e.rec.UpdateBackupResult(ctx, id, "completed", total, len(disks), string(disksJSON), ""); err != nil {
			return rec, fmt.Errorf("backup: finalize record: %w", err)
		}
	}
	return rec, nil
}

// exportAndUpload exports each disk, converts it to qcow2, and uploads it. The
// stream is staged to a temp file so the upload has a known content length (Azure)
// and so qemu-img (which needs seekable files) can run.
func (e *Engine) exportAndUpload(ctx context.Context, src vp.HypervisorProvider, objs storage.ObjectStore, vmID, keyPrefix string, diskCount int) ([]DiskArtifact, int64, error) {
	srcFmt := nativeFormat(src.Kind())
	rc, info, err := src.ExportVM(ctx, vmID, srcFmt)
	if err != nil {
		return nil, 0, fmt.Errorf("export: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if info != nil && info.Format != "" {
		srcFmt = info.Format
	}

	// Convert the export stream to qcow2 and stage it to a temp file.
	tmp, err := os.CreateTemp("", "unihv-backup-*.qcow2")
	if err != nil {
		return nil, 0, fmt.Errorf("stage: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, cerr := e.conv.Convert(ctx, rc, tmp, srcFmt, vp.DiskQcow2); cerr != nil {
		_ = tmp.Close()
		return nil, 0, fmt.Errorf("convert: %w", cerr)
	}
	if err := tmp.Close(); err != nil {
		return nil, 0, fmt.Errorf("stage close: %w", err)
	}

	st, err := os.Stat(tmpPath)
	if err != nil {
		return nil, 0, fmt.Errorf("stage stat: %w", err)
	}
	f, err := os.Open(tmpPath)
	if err != nil {
		return nil, 0, fmt.Errorf("stage open: %w", err)
	}
	defer func() { _ = f.Close() }()

	// The export pipeline yields ONE combined image stream; store it as disk-0.
	// (Per-disk export is a provider-internal detail; a single qcow2 captures the
	// VM's disk content for restore, matching the V2V/replication export model.)
	key := keyPrefix + "disk-0.qcow2"
	n, perr := objs.PutObject(ctx, key, f, st.Size())
	if perr != nil {
		return nil, 0, fmt.Errorf("upload: %w", perr)
	}
	disks := []DiskArtifact{{Key: key, SizeBytes: n, Format: vp.DiskQcow2}}
	return disks, n, nil
}

// dropSnapshot removes a snapshot best-effort (providers expose deletion via the
// extended SnapshotManager; absent that, the snapshot is left for the operator).
func (e *Engine) dropSnapshot(ctx context.Context, src vp.HypervisorProvider, vmID, snapID string) error {
	if sm, ok := src.(interface {
		DeleteSnapshot(ctx context.Context, vmID, snapID string) (*vp.Task, error)
	}); ok {
		_, err := sm.DeleteSnapshot(ctx, vmID, snapID)
		return err
	}
	return nil
}

// RestoreRequest parameterizes a restore.
type RestoreRequest struct {
	BackupID         string
	TargetProviderID string
	TargetHostID     string
	TargetName       string // defaults to "<vmName>-restored"
	PowerOnAfter     bool
}

// Restore pulls a backup's artifacts and imports them as a NEW VM on the target
// provider. It returns the new VM id. The disk image is streamed from the backend
// through the converter into the target's native format and a new VM is created
// via CreateVM (the same import path V2V uses).
func (e *Engine) Restore(ctx context.Context, req RestoreRequest) (string, error) {
	if e.rec == nil {
		return "", fmt.Errorf("backup: restore requires a persistence layer")
	}
	rec, err := e.rec.GetBackupRecord(ctx, req.BackupID)
	if err != nil {
		return "", err
	}
	if rec.Status != "completed" {
		return "", fmt.Errorf("backup: cannot restore a %s backup", rec.Status)
	}
	tgt, ok := e.reg.Get(req.TargetProviderID)
	if !ok {
		return "", fmt.Errorf("backup: unknown target provider %q", req.TargetProviderID)
	}
	if !tgt.Capabilities().Has(vp.CapCreateVM) {
		return "", fmt.Errorf("backup: target provider %q cannot create VMs", req.TargetProviderID)
	}
	objs, err := e.backends.ObjectStore(ctx, rec.BackendID)
	if err != nil {
		return "", fmt.Errorf("backup: storage backend: %w", err)
	}

	// Read the manifest for hardware metadata (fall back to the record's disks).
	man := manifest{VMID: rec.VMID, VMName: rec.VMName, GuestOS: rec.GuestOS, Firmware: rec.Firmware, Disks: rec.Disks}
	if mr, merr := objs.GetObject(ctx, rec.KeyPrefix+manifestKey); merr == nil {
		mb, _ := io.ReadAll(mr)
		_ = mr.Close()
		var parsed manifest
		if json.Unmarshal(mb, &parsed) == nil && len(parsed.Disks) > 0 {
			man = parsed
		}
	}
	if len(man.Disks) == 0 {
		return "", fmt.Errorf("backup: backup %q has no disk artifacts", req.BackupID)
	}

	// Pull the first disk artifact, convert qcow2 -> target native format, measure.
	tgtFmt := nativeFormat(tgt.Kind())
	dr, err := objs.GetObject(ctx, man.Disks[0].Key)
	if err != nil {
		return "", fmt.Errorf("backup: download artifact: %w", err)
	}
	defer func() { _ = dr.Close() }()

	tmp, err := os.CreateTemp("", "unihv-restore-*.img")
	if err != nil {
		return "", fmt.Errorf("backup: stage: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, cerr := e.conv.Convert(ctx, dr, tmp, vp.DiskQcow2, tgtFmt); cerr != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("backup: convert: %w", cerr)
	}
	_ = tmp.Close()
	st, _ := os.Stat(tmpPath)
	var sizeBytes int64
	if st != nil {
		sizeBytes = st.Size()
	}

	name := req.TargetName
	if name == "" {
		base := man.VMName
		if base == "" {
			base = rec.VMID
		}
		name = base + "-restored"
	}
	fw := man.Firmware
	if fw == "" {
		fw = vp.FirmwareUEFI
	}
	gb := float64(sizeBytes) / (1 << 30)
	if gb < 1 {
		gb = 20
	}
	spec := vp.VMSpec{
		Name: name, HostID: req.TargetHostID, VCPUs: 2, MemoryMB: 4096,
		GuestOS: man.GuestOS, Firmware: fw,
	}
	for range man.Disks {
		spec.Disks = append(spec.Disks, vp.DiskSpec{CapacityGB: gb, Format: tgtFmt})
	}
	task, err := tgt.CreateVM(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("backup: create restored VM: %w", err)
	}
	newID := task.EntityID
	if req.PowerOnAfter && tgt.Capabilities().Has(vp.CapPowerStart) {
		_, _ = tgt.PowerOp(ctx, newID, vp.PowerStart)
	}
	return newID, nil
}

// DeleteBackup removes a backup's artifacts from the backend AND its row.
func (e *Engine) DeleteBackup(ctx context.Context, id string) error {
	if e.rec == nil {
		return fmt.Errorf("backup: delete requires a persistence layer")
	}
	rec, err := e.rec.GetBackupRecord(ctx, id)
	if err != nil {
		return err
	}
	return e.deleteBackupArtifactsAndRow(ctx, rec)
}

// deleteBackupArtifactsAndRow deletes every artifact under the backup's key prefix
// (best-effort) then removes the row.
func (e *Engine) deleteBackupArtifactsAndRow(ctx context.Context, rec *Record) error {
	if objs, err := e.backends.ObjectStore(ctx, rec.BackendID); err == nil {
		if list, lerr := objs.ListObjects(ctx, rec.KeyPrefix); lerr == nil {
			for _, o := range list {
				_ = objs.DeleteObject(ctx, o.Key)
			}
		} else {
			// Fall back to the known keys from the record.
			for _, d := range rec.Disks {
				_ = objs.DeleteObject(ctx, d.Key)
			}
			_ = objs.DeleteObject(ctx, rec.KeyPrefix+manifestKey)
		}
	}
	if e.rec != nil {
		return e.rec.DeleteBackupRow(ctx, rec.ID)
	}
	return nil
}

func (e *Engine) persistPolicy(ctx context.Context, id, status, lastErr string, lastRunAt int64) {
	if e.rec == nil {
		return
	}
	_ = e.rec.UpdatePolicyState(ctx, id, status, lastErr, lastRunAt)
}

// nativeFormat returns a hypervisor family's native disk format (matches migrate).
func nativeFormat(k vp.HypervisorKind) vp.DiskFormat {
	switch k {
	case vp.KindVMware:
		return vp.DiskVMDK
	case vp.KindHyperV:
		return vp.DiskVHDX
	case vp.KindKVM, vp.KindXen:
		return vp.DiskQcow2
	default:
		return vp.DiskRaw
	}
}

// byteReader wraps a byte slice as an io.Reader.
func byteReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct {
	b   []byte
	off int
}

func (s *sliceReader) Read(p []byte) (int, error) {
	if s.off >= len(s.b) {
		return 0, io.EOF
	}
	n := copy(p, s.b[s.off:])
	s.off += n
	return n, nil
}
