package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ResourcePool is a row of resource_pools (Lot 5A): a named, provider-scoped
// grouping of VMs with an AGGREGATE CPU/memory shares + limit budget. VMs join via
// the "unihv.pool=<id>" domain label. The budget is an advisory/reported allocation
// contract (plain libvirt has no native parent-cgroup pool); the pool is persisted,
// assignable, and its budget vs. members is reported by the API.
type ResourcePool struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ProviderID  string `json:"providerId"`
	CPUShares   int64  `json:"cpuShares"`
	CPULimitMHz int64  `json:"cpuLimitMhz"`
	MemShares   int64  `json:"memShares"`
	MemLimitMB  int64  `json:"memLimitMb"`
	Notes       string `json:"notes,omitempty"`
	CreatedAt   int64  `json:"createdAt"`
	UpdatedAt   int64  `json:"updatedAt"`
}

const resourcePoolCols = `id, name, provider_id, cpu_shares, cpu_limit_mhz, mem_shares, mem_limit_mb, notes, created_at, updated_at`

func scanResourcePool(row interface{ Scan(...any) error }) (*ResourcePool, error) {
	var p ResourcePool
	var notes sql.NullString
	if err := row.Scan(&p.ID, &p.Name, &p.ProviderID, &p.CPUShares, &p.CPULimitMHz,
		&p.MemShares, &p.MemLimitMB, &notes, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Notes = notes.String
	return &p, nil
}

// ListResourcePools returns all pools (optionally filtered to one provider) ordered
// by name. providerID == "" lists every pool.
func (s *Store) ListResourcePools(ctx context.Context, providerID string) ([]*ResourcePool, error) {
	q := `SELECT ` + resourcePoolCols + ` FROM resource_pools`
	args := []any{}
	if providerID != "" {
		q += ` WHERE provider_id = ?`
		args = append(args, providerID)
	}
	q += ` ORDER BY name ASC`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*ResourcePool
	for rows.Next() {
		p, err := scanResourcePool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetResourcePool returns one pool by id.
func (s *Store) GetResourcePool(ctx context.Context, id string) (*ResourcePool, error) {
	return scanResourcePool(s.db.QueryRowContext(ctx,
		`SELECT `+resourcePoolCols+` FROM resource_pools WHERE id = ?`, id))
}

// CreateResourcePool inserts a new pool.
func (s *Store) CreateResourcePool(ctx context.Context, p *ResourcePool) error {
	now := time.Now().Unix()
	p.CreatedAt, p.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO resource_pools
			(id, name, provider_id, cpu_shares, cpu_limit_mhz, mem_shares, mem_limit_mb, notes, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.ProviderID, p.CPUShares, p.CPULimitMHz, p.MemShares, p.MemLimitMB,
		nullStr(p.Notes), p.CreatedAt, p.UpdatedAt)
	return err
}

// UpdateResourcePool updates a pool's budget + notes (name/provider are immutable).
func (s *Store) UpdateResourcePool(ctx context.Context, p *ResourcePool) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE resource_pools
		 SET cpu_shares = ?, cpu_limit_mhz = ?, mem_shares = ?, mem_limit_mb = ?, notes = ?, updated_at = ?
		 WHERE id = ?`,
		p.CPUShares, p.CPULimitMHz, p.MemShares, p.MemLimitMB, nullStr(p.Notes), time.Now().Unix(), p.ID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteResourcePool removes a pool by id.
func (s *Store) DeleteResourcePool(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM resource_pools WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
