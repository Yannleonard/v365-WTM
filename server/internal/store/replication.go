package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ReplicationPolicy is a row of replication_policies: one cross-hypervisor VM
// replication definition (replicate SourceVMID from SourceProviderID to a DIFFERENT
// TargetProviderID every IntervalSeconds for DR). The replication engine
// (server/internal/replication) holds the live runtime cycle state/history; this is
// the durable definition + last-known summary, mirroring the HypervisorConn pattern.
// IntervalSeconds is the RPO target. Status is idle|syncing|error|degraded|failed_over.
type ReplicationPolicy struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	SourceProviderID string `json:"sourceProviderId"`
	SourceVMID       string `json:"sourceVmId"`
	TargetProviderID string `json:"targetProviderId"`
	TargetHostID     string `json:"targetHostId,omitempty"`
	IntervalSeconds  int    `json:"intervalSeconds"`
	Retain           int    `json:"retain"`
	Enabled          bool   `json:"enabled"`
	Status           string `json:"status"`
	LastSyncAt       int64  `json:"lastSyncAt,omitempty"`
	ReplicaVMID      string `json:"replicaVmId,omitempty"`
	LastError        string `json:"lastError,omitempty"`
	CreatedAt        int64  `json:"createdAt"`
	UpdatedAt        int64  `json:"updatedAt"`
}

const replPolicyCols = `id, name, source_provider_id, source_vm_id, target_provider_id, target_host_id, ` +
	`interval_seconds, retain, enabled, status, last_sync_at, replica_vm_id, last_error, created_at, updated_at`

func scanReplPolicy(row interface{ Scan(...any) error }) (*ReplicationPolicy, error) {
	var p ReplicationPolicy
	var enabled int
	var targetHost, replicaVM, lastErr sql.NullString
	var lastSync sql.NullInt64
	if err := row.Scan(&p.ID, &p.Name, &p.SourceProviderID, &p.SourceVMID, &p.TargetProviderID,
		&targetHost, &p.IntervalSeconds, &p.Retain, &enabled, &p.Status,
		&lastSync, &replicaVM, &lastErr, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.TargetHostID = targetHost.String
	p.Enabled = enabled != 0
	p.LastSyncAt = lastSync.Int64
	p.ReplicaVMID = replicaVM.String
	p.LastError = lastErr.String
	return &p, nil
}

// ListReplicationPolicies returns all policies ordered by name.
func (s *Store) ListReplicationPolicies(ctx context.Context) ([]*ReplicationPolicy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+replPolicyCols+` FROM replication_policies ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*ReplicationPolicy
	for rows.Next() {
		p, err := scanReplPolicy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetReplicationPolicy returns one policy by id.
func (s *Store) GetReplicationPolicy(ctx context.Context, id string) (*ReplicationPolicy, error) {
	return scanReplPolicy(s.db.QueryRowContext(ctx,
		`SELECT `+replPolicyCols+` FROM replication_policies WHERE id = ?`, id))
}

// CreateReplicationPolicy inserts a new policy.
func (s *Store) CreateReplicationPolicy(ctx context.Context, p *ReplicationPolicy) error {
	now := time.Now().Unix()
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Status == "" {
		p.Status = "idle"
	}
	if p.IntervalSeconds <= 0 {
		p.IntervalSeconds = 300
	}
	if p.Retain <= 0 {
		p.Retain = 5
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO replication_policies
			(id, name, source_provider_id, source_vm_id, target_provider_id, target_host_id,
			 interval_seconds, retain, enabled, status, last_sync_at, replica_vm_id, last_error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL, ?, ?)`,
		p.ID, p.Name, p.SourceProviderID, p.SourceVMID, p.TargetProviderID, nullStr(p.TargetHostID),
		p.IntervalSeconds, p.Retain, boolInt(p.Enabled), p.Status, p.CreatedAt, p.UpdatedAt)
	return err
}

// UpdateReplicationPolicyState persists the runtime summary after a cycle (or a
// failover): status, last_error, and — when non-empty/non-zero — the last sync time
// and the replica VM id (once the first cycle has created it). Mirrors
// UpdateHypervisorConnStatus.
func (s *Store) UpdateReplicationPolicyState(ctx context.Context, id, status, lastErr, replicaVMID string, lastSyncAt int64) error {
	var sync any
	if lastSyncAt > 0 {
		sync = lastSyncAt
	}
	var replica any
	if replicaVMID != "" {
		replica = replicaVMID
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE replication_policies
		 SET status = ?, last_error = ?,
		     last_sync_at = COALESCE(?, last_sync_at),
		     replica_vm_id = COALESCE(?, replica_vm_id),
		     updated_at = ?
		 WHERE id = ?`,
		status, nullStr(lastErr), sync, replica, time.Now().Unix(), id)
	return err
}

// DeleteReplicationPolicy removes a policy by id.
func (s *Store) DeleteReplicationPolicy(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM replication_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
