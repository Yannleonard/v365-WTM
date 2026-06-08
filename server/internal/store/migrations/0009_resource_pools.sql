-- UniHV resource pools (Lot 5A, KVM Backend Engineer).
-- A resource pool is a lightweight, named grouping of VMs with AGGREGATE CPU/memory
-- shares + limits. VMs join a pool via the "unihv.pool=<id>" label on the domain.
-- Full hard enforcement (a parent cgroup for all member VMs) is a host-level stretch
-- that plain libvirt does not model natively, so the pool's limits here are an
-- advisory/reported allocation contract: the pool is persisted, assignable, and its
-- aggregate budget vs. members' usage is reported. provider_id scopes a pool to one
-- hypervisor provider (pools do not span providers). *_at columns are unix seconds.

CREATE TABLE IF NOT EXISTS resource_pools (
    id                 TEXT PRIMARY KEY,            -- UUID
    name               TEXT NOT NULL,               -- human label (unique per provider)
    provider_id        TEXT NOT NULL,               -- vprovider id this pool scopes to
    cpu_shares         INTEGER NOT NULL DEFAULT 0,  -- aggregate CPU shares (relative weight)
    cpu_limit_mhz      INTEGER NOT NULL DEFAULT 0,  -- aggregate CPU hard cap (MHz; 0 = unlimited)
    mem_shares         INTEGER NOT NULL DEFAULT 0,  -- aggregate memory shares (relative weight)
    mem_limit_mb       INTEGER NOT NULL DEFAULT 0,  -- aggregate memory hard cap (MB; 0 = unlimited)
    notes              TEXT,                         -- optional free-text description
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_respool_provider ON resource_pools(provider_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_respool_provider_name ON resource_pools(provider_id, name);
