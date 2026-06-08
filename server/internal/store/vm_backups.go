package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// VMBackup is a row of vm_backups: one produced VM backup artifact set (snapshot
// -> exported disk(s) -> uploaded to a storage backend under KeyPrefix). DisksJSON
// is a JSON array of {key,sizeBytes,format} entries — the artifacts to pull on
// restore. PolicyID is empty for ad-hoc ("Back up now") backups. Mirrors the
// replication/storage_backend persistence patterns. *At fields are unix seconds.
type VMBackup struct {
	ID         string `json:"id"`
	VMID       string `json:"vmId"`
	VMName     string `json:"vmName,omitempty"`
	ProviderID string `json:"providerId"`
	BackendID  string `json:"backendId"`
	PolicyID   string `json:"policyId,omitempty"`
	KeyPrefix  string `json:"keyPrefix"`
	SizeBytes  int64  `json:"sizeBytes"`
	DiskCount  int    `json:"diskCount"`
	DisksJSON  string `json:"disks,omitempty"` // raw JSON array string
	GuestOS    string `json:"guestOs,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
}

const vmBackupCols = `id, vm_id, vm_name, provider_id, backend_id, policy_id, key_prefix, ` +
	`size_bytes, disk_count, disks_json, guest_os, firmware, status, error, created_at`

func scanVMBackup(row interface{ Scan(...any) error }) (*VMBackup, error) {
	var b VMBackup
	var vmName, policyID, disks, guestOS, firmware, errMsg sql.NullString
	if err := row.Scan(&b.ID, &b.VMID, &vmName, &b.ProviderID, &b.BackendID, &policyID,
		&b.KeyPrefix, &b.SizeBytes, &b.DiskCount, &disks, &guestOS, &firmware,
		&b.Status, &errMsg, &b.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	b.VMName = vmName.String
	b.PolicyID = policyID.String
	b.DisksJSON = disks.String
	b.GuestOS = guestOS.String
	b.Firmware = firmware.String
	b.Error = errMsg.String
	return &b, nil
}

// CreateVMBackup inserts a backup row (typically in pending status, finalized via
// UpdateVMBackupResult once the artifacts are uploaded).
func (s *Store) CreateVMBackup(ctx context.Context, b *VMBackup) error {
	if b.CreatedAt == 0 {
		b.CreatedAt = time.Now().Unix()
	}
	if b.Status == "" {
		b.Status = "pending"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vm_backups (`+vmBackupCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		b.ID, b.VMID, nullStr(b.VMName), b.ProviderID, b.BackendID, nullStr(b.PolicyID),
		b.KeyPrefix, b.SizeBytes, b.DiskCount, nullStr(b.DisksJSON), nullStr(b.GuestOS),
		nullStr(b.Firmware), b.Status, nullStr(b.Error), b.CreatedAt)
	return err
}

// UpdateVMBackupResult finalizes a backup row after upload: status, totals, disk
// artifact list and optional error.
func (s *Store) UpdateVMBackupResult(ctx context.Context, id, status string, sizeBytes int64, diskCount int, disksJSON, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE vm_backups SET status = ?, size_bytes = ?, disk_count = ?, disks_json = ?, error = ? WHERE id = ?`,
		status, sizeBytes, diskCount, nullStr(disksJSON), nullStr(errMsg), id)
	return err
}

// GetVMBackup returns one backup by id.
func (s *Store) GetVMBackup(ctx context.Context, id string) (*VMBackup, error) {
	return scanVMBackup(s.db.QueryRowContext(ctx,
		`SELECT `+vmBackupCols+` FROM vm_backups WHERE id = ?`, id))
}

// ListVMBackups returns backups, newest first. When vmID != "" it filters to one VM.
func (s *Store) ListVMBackups(ctx context.Context, vmID string) ([]*VMBackup, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if vmID != "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+vmBackupCols+` FROM vm_backups WHERE vm_id = ? ORDER BY created_at DESC`, vmID)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT `+vmBackupCols+` FROM vm_backups ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*VMBackup
	for rows.Next() {
		b, err := scanVMBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ListVMBackupsForPolicy returns a policy's completed backups, OLDEST first (so
// retention pruning can drop from the front).
func (s *Store) ListVMBackupsForPolicy(ctx context.Context, policyID string) ([]*VMBackup, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+vmBackupCols+` FROM vm_backups WHERE policy_id = ? AND status = 'completed' ORDER BY created_at ASC`, policyID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*VMBackup
	for rows.Next() {
		b, err := scanVMBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteVMBackup removes a backup row by id (artifact deletion is the caller's job).
func (s *Store) DeleteVMBackup(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM vm_backups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// vm_backup_policies
// ---------------------------------------------------------------------------

// VMBackupPolicy is a row of vm_backup_policies: a scheduled-backup definition.
type VMBackupPolicy struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	ProviderID      string `json:"providerId"`
	VMID            string `json:"vmId"`
	BackendID       string `json:"backendId"`
	IntervalSeconds int    `json:"intervalSeconds"`
	RetentionCount  int    `json:"retentionCount"`
	Enabled         bool   `json:"enabled"`
	Status          string `json:"status"`
	LastRunAt       int64  `json:"lastRunAt,omitempty"`
	LastError       string `json:"lastError,omitempty"`
	CreatedAt       int64  `json:"createdAt"`
	UpdatedAt       int64  `json:"updatedAt"`
}

const vmBackupPolicyCols = `id, name, provider_id, vm_id, backend_id, interval_seconds, ` +
	`retention_count, enabled, status, last_run_at, last_error, created_at, updated_at`

func scanVMBackupPolicy(row interface{ Scan(...any) error }) (*VMBackupPolicy, error) {
	var p VMBackupPolicy
	var enabled int
	var lastErr sql.NullString
	var lastRun sql.NullInt64
	if err := row.Scan(&p.ID, &p.Name, &p.ProviderID, &p.VMID, &p.BackendID,
		&p.IntervalSeconds, &p.RetentionCount, &enabled, &p.Status,
		&lastRun, &lastErr, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Enabled = enabled != 0
	p.LastRunAt = lastRun.Int64
	p.LastError = lastErr.String
	return &p, nil
}

// ListVMBackupPolicies returns all policies ordered by name.
func (s *Store) ListVMBackupPolicies(ctx context.Context) ([]*VMBackupPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+vmBackupPolicyCols+` FROM vm_backup_policies ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*VMBackupPolicy
	for rows.Next() {
		p, err := scanVMBackupPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetVMBackupPolicy returns one policy by id.
func (s *Store) GetVMBackupPolicy(ctx context.Context, id string) (*VMBackupPolicy, error) {
	return scanVMBackupPolicy(s.db.QueryRowContext(ctx,
		`SELECT `+vmBackupPolicyCols+` FROM vm_backup_policies WHERE id = ?`, id))
}

// CreateVMBackupPolicy inserts a new policy with sane defaults.
func (s *Store) CreateVMBackupPolicy(ctx context.Context, p *VMBackupPolicy) error {
	now := time.Now().Unix()
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Status == "" {
		p.Status = "idle"
	}
	if p.IntervalSeconds <= 0 {
		p.IntervalSeconds = 86400
	}
	if p.RetentionCount <= 0 {
		p.RetentionCount = 7
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vm_backup_policies
			(id, name, provider_id, vm_id, backend_id, interval_seconds, retention_count, enabled, status, last_run_at, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		p.ID, p.Name, p.ProviderID, p.VMID, p.BackendID, p.IntervalSeconds, p.RetentionCount,
		boolInt(p.Enabled), p.Status, p.CreatedAt, p.UpdatedAt)
	return err
}

// UpdateVMBackupPolicyState persists the runtime summary after a run: status,
// last_error and (when >0) the last successful run time.
func (s *Store) UpdateVMBackupPolicyState(ctx context.Context, id, status, lastErr string, lastRunAt int64) error {
	var run any
	if lastRunAt > 0 {
		run = lastRunAt
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE vm_backup_policies
		 SET status = ?, last_error = ?, last_run_at = COALESCE(?, last_run_at), updated_at = ?
		 WHERE id = ?`,
		status, nullStr(lastErr), run, time.Now().Unix(), id)
	return err
}

// DeleteVMBackupPolicy removes a policy by id.
func (s *Store) DeleteVMBackupPolicy(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM vm_backup_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
