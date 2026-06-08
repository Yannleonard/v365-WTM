package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// HypervisorConn is a row of hypervisor_connections: one registered hypervisor
// endpoint (standalone host or cluster). SecretEnc (sealed credential) is NEVER
// serialized (json:"-"); the API surfaces only HasSecret. Sealing/opening happen
// in the API layer via authz.SealSecret/OpenSecret (the store never imports authz,
// avoiding an import cycle). Kind is kvm|hyperv|vmware|xen.
type HypervisorConn struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	SecretEnc   []byte `json:"-"`
	InsecureTLS bool   `json:"insecureTls"`
	Enabled     bool   `json:"enabled"`
	Status      string `json:"status"`
	LastError   string `json:"lastError,omitempty"`
	LastSeenAt  int64  `json:"lastSeenAt,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

// HasSecret reports whether a sealed credential is stored.
func (h *HypervisorConn) HasSecret() bool { return len(h.SecretEnc) > 0 }

const hvConnCols = `id, name, kind, endpoint, username, secret_enc, insecure_tls, enabled, status, last_error, last_seen_at, created_at, updated_at`

func scanHVConn(row interface{ Scan(...any) error }) (*HypervisorConn, error) {
	var h HypervisorConn
	var secret []byte
	var insecure, enabled int
	var lastErr sql.NullString
	var lastSeen sql.NullInt64
	if err := row.Scan(&h.ID, &h.Name, &h.Kind, &h.Endpoint, &h.Username, &secret,
		&insecure, &enabled, &h.Status, &lastErr, &lastSeen, &h.CreatedAt, &h.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	h.SecretEnc = secret
	h.InsecureTLS = insecure != 0
	h.Enabled = enabled != 0
	h.LastError = lastErr.String
	h.LastSeenAt = lastSeen.Int64
	return &h, nil
}

// ListHypervisorConns returns all connections ordered by name.
func (s *Store) ListHypervisorConns(ctx context.Context) ([]*HypervisorConn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+hvConnCols+` FROM hypervisor_connections ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*HypervisorConn
	for rows.Next() {
		h, err := scanHVConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetHypervisorConn returns one connection by id.
func (s *Store) GetHypervisorConn(ctx context.Context, id string) (*HypervisorConn, error) {
	return scanHVConn(s.db.QueryRowContext(ctx, `SELECT `+hvConnCols+` FROM hypervisor_connections WHERE id = ?`, id))
}

// CreateHypervisorConn inserts a new connection (secretEnc may be nil).
func (s *Store) CreateHypervisorConn(ctx context.Context, h *HypervisorConn) error {
	now := time.Now().Unix()
	h.CreatedAt, h.UpdatedAt = now, now
	if h.Status == "" {
		h.Status = "pending"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO hypervisor_connections
			(id, name, kind, endpoint, username, secret_enc, insecure_tls, enabled, status, last_error, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		h.ID, h.Name, h.Kind, h.Endpoint, h.Username, h.SecretEnc,
		boolInt(h.InsecureTLS), boolInt(h.Enabled), h.Status, h.CreatedAt, h.UpdatedAt)
	return err
}

// UpdateHypervisorConnStatus updates the runtime status/last_error/last_seen.
func (s *Store) UpdateHypervisorConnStatus(ctx context.Context, id, status, lastErr string) error {
	var seen any
	if status == "connected" {
		seen = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE hypervisor_connections SET status = ?, last_error = ?, last_seen_at = COALESCE(?, last_seen_at), updated_at = ? WHERE id = ?`,
		status, nullStr(lastErr), seen, time.Now().Unix(), id)
	return err
}

// DeleteHypervisorConn removes a connection by id.
func (s *Store) DeleteHypervisorConn(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM hypervisor_connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
