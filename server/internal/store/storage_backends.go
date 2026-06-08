package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// StorageBackend is a row of storage_backends: one registered pluggable storage
// backend (SAN/NAS via libvirt pool, or cloud object store). SecretEnc (sealed
// credential) is NEVER serialized (json:"-"); the API surfaces only HasSecret.
// Sealing/opening happen in the API layer via authz.SealSecret/OpenSecret (the
// store never imports authz, avoiding an import cycle). Type is one of
// nfs|iscsi|smb|azureblob|s3. Mirrors HypervisorConn exactly.
type StorageBackend struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	Endpoint   string `json:"endpoint"`
	Target     string `json:"target"`
	Username   string `json:"username"`
	SecretEnc  []byte `json:"-"`
	Region     string `json:"region,omitempty"`
	ProviderID string `json:"providerId,omitempty"`
	Options    string `json:"options,omitempty"` // raw JSON string
	Enabled    bool   `json:"enabled"`
	Status     string `json:"status"`
	LastError  string `json:"lastError,omitempty"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
	CreatedAt  int64  `json:"createdAt"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// HasSecret reports whether a sealed credential is stored.
func (b *StorageBackend) HasSecret() bool { return len(b.SecretEnc) > 0 }

const storageBackendCols = `id, name, type, endpoint, target, username, secret_enc, region, provider_id, options, enabled, status, last_error, last_seen_at, created_at, updated_at`

func scanStorageBackend(row interface{ Scan(...any) error }) (*StorageBackend, error) {
	var b StorageBackend
	var secret []byte
	var enabled int
	var endpoint, target, username, region, providerID, options, lastErr sql.NullString
	var lastSeen sql.NullInt64
	if err := row.Scan(&b.ID, &b.Name, &b.Type, &endpoint, &target, &username, &secret,
		&region, &providerID, &options, &enabled, &b.Status, &lastErr, &lastSeen,
		&b.CreatedAt, &b.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	b.Endpoint = endpoint.String
	b.Target = target.String
	b.Username = username.String
	b.SecretEnc = secret
	b.Region = region.String
	b.ProviderID = providerID.String
	b.Options = options.String
	b.Enabled = enabled != 0
	b.LastError = lastErr.String
	b.LastSeenAt = lastSeen.Int64
	return &b, nil
}

// ListStorageBackends returns all storage backends ordered by name.
func (s *Store) ListStorageBackends(ctx context.Context) ([]*StorageBackend, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+storageBackendCols+` FROM storage_backends ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*StorageBackend
	for rows.Next() {
		b, err := scanStorageBackend(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetStorageBackend returns one backend by id.
func (s *Store) GetStorageBackend(ctx context.Context, id string) (*StorageBackend, error) {
	return scanStorageBackend(s.db.QueryRowContext(ctx, `SELECT `+storageBackendCols+` FROM storage_backends WHERE id = ?`, id))
}

// CreateStorageBackend inserts a new backend (secretEnc may be nil).
func (s *Store) CreateStorageBackend(ctx context.Context, b *StorageBackend) error {
	now := time.Now().Unix()
	b.CreatedAt, b.UpdatedAt = now, now
	if b.Status == "" {
		b.Status = "pending"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO storage_backends
			(id, name, type, endpoint, target, username, secret_enc, region, provider_id, options, enabled, status, last_error, last_seen_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?)`,
		b.ID, b.Name, b.Type, nullStr(b.Endpoint), nullStr(b.Target), nullStr(b.Username),
		b.SecretEnc, nullStr(b.Region), nullStr(b.ProviderID), nullStr(b.Options),
		boolInt(b.Enabled), b.Status, b.CreatedAt, b.UpdatedAt)
	return err
}

// UpdateStorageBackendStatus updates the runtime status/last_error/last_seen.
func (s *Store) UpdateStorageBackendStatus(ctx context.Context, id, status, lastErr string) error {
	var seen any
	if status == "connected" {
		seen = time.Now().Unix()
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE storage_backends SET status = ?, last_error = ?, last_seen_at = COALESCE(?, last_seen_at), updated_at = ? WHERE id = ?`,
		status, nullStr(lastErr), seen, time.Now().Unix(), id)
	return err
}

// DeleteStorageBackend removes a backend by id.
func (s *Store) DeleteStorageBackend(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM storage_backends WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
