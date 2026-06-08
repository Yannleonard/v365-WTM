package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// APIToken is a row of api_tokens: one scoped personal access token. The raw token
// is never stored (only TokenHash, the hex SHA-256 of the raw value). Permissions
// is the JSON-decoded subset of the owner's grants the bearer-auth path intersects
// with the owner's roles. The raw value is surfaced ONCE at create time via a
// separate field on the API response, never from the store.
type APIToken struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	UserID      string   `json:"userId"`
	TokenHash   string   `json:"-"`
	Permissions []string `json:"permissions"`
	ExpiresAt   *int64   `json:"expiresAt,omitempty"`
	LastUsedAt  *int64   `json:"lastUsedAt,omitempty"`
	RevokedAt   *int64   `json:"-"`
	CreatedAt   int64    `json:"createdAt"`
}

const apiTokenCols = `id, name, user_id, token_hash, permissions, expires_at, last_used_at, revoked_at, created_at`

func scanAPIToken(row interface{ Scan(...any) error }) (*APIToken, error) {
	var t APIToken
	var perms string
	var expires, lastUsed, revoked sql.NullInt64
	if err := row.Scan(&t.ID, &t.Name, &t.UserID, &t.TokenHash, &perms,
		&expires, &lastUsed, &revoked, &t.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(perms), &t.Permissions)
	if t.Permissions == nil {
		t.Permissions = []string{}
	}
	if expires.Valid {
		v := expires.Int64
		t.ExpiresAt = &v
	}
	if lastUsed.Valid {
		v := lastUsed.Int64
		t.LastUsedAt = &v
	}
	if revoked.Valid {
		v := revoked.Int64
		t.RevokedAt = &v
	}
	return &t, nil
}

// CreateAPIToken inserts a new token row. The caller passes the hex SHA-256 of the
// raw token as TokenHash (the raw value is shown to the user once and discarded).
func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken) error {
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().Unix()
	}
	permsJSON, err := json.Marshal(t.Permissions)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO api_tokens (`+apiTokenCols+`)
		 VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?)`,
		t.ID, t.Name, t.UserID, t.TokenHash, string(permsJSON),
		nullInt(t.ExpiresAt), t.CreatedAt)
	return err
}

// GetAPITokenByHash returns the ACTIVE, non-expired token for a token hash, or
// ErrNotFound. This is the bearer-auth lookup; it filters revoked + expired rows
// at the SQL level so a stale credential never authenticates.
func (s *Store) GetAPITokenByHash(ctx context.Context, tokenHash string) (*APIToken, error) {
	now := time.Now().Unix()
	return scanAPIToken(s.db.QueryRowContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens
		 WHERE token_hash = ? AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > ?)`,
		tokenHash, now))
}

// GetAPIToken returns one token by id (active or not).
func (s *Store) GetAPIToken(ctx context.Context, id string) (*APIToken, error) {
	return scanAPIToken(s.db.QueryRowContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens WHERE id = ?`, id))
}

// ListAPITokensForUser returns a user's ACTIVE (non-revoked) tokens, newest first.
// The raw token is never included (it is not stored).
func (s *Store) ListAPITokensForUser(ctx context.Context, userID string) ([]*APIToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+apiTokenCols+` FROM api_tokens
		 WHERE user_id = ? AND revoked_at IS NULL
		 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*APIToken
	for rows.Next() {
		t, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchAPIToken records last_used_at on a successful bearer auth (best-effort).
func (s *Store) TouchAPIToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

// RevokeAPIToken revokes a token by id, scoped to its owner so a user can only
// revoke their OWN tokens. Returns ErrNotFound when no matching active row exists.
func (s *Store) RevokeAPIToken(ctx context.Context, id, userID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_tokens SET revoked_at = ?
		 WHERE id = ? AND user_id = ? AND revoked_at IS NULL`,
		time.Now().Unix(), id, userID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
