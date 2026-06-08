-- UniHV cross-hypervisor VM replication policies (DR/Replication Engineer).
-- One row per replication policy: continuously/periodically replicate a VM from a
-- SOURCE hypervisor provider to a DIFFERENT TARGET provider (e.g. KVM -> ESXi) for
-- disaster recovery. The replication engine (server/internal/replication) drives a
-- scheduled V2V cycle (snapshot -> export -> convert -> create/update replica) per
-- policy on its interval (the RPO target). Runtime cycle state/history lives
-- in-memory in the engine; this table is the DURABLE definition + last-known summary
-- so enabled policies are reloaded and restarted at boot (like hypervisor_connections).
-- *_at columns are unix epoch seconds (UTC). The replica VM id is filled after the
-- first successful cycle creates the replica on the target.

CREATE TABLE IF NOT EXISTS replication_policies (
    id                 TEXT PRIMARY KEY,            -- UUID
    name               TEXT NOT NULL,               -- human label
    source_provider_id TEXT NOT NULL,               -- vprovider id of the SOURCE hypervisor
    source_vm_id       TEXT NOT NULL,               -- VM to replicate (source-native id)
    target_provider_id TEXT NOT NULL,               -- vprovider id of the TARGET hypervisor (MUST differ)
    target_host_id     TEXT,                         -- placement host on the target (optional)
    interval_seconds   INTEGER NOT NULL DEFAULT 300,-- RPO target: seconds between cycles
    retain             INTEGER NOT NULL DEFAULT 5,  -- snapshot/cycle-history retention
    enabled            INTEGER NOT NULL DEFAULT 1,  -- whether to schedule on startup
    status             TEXT NOT NULL DEFAULT 'idle',-- idle|syncing|error|degraded|failed_over
    last_sync_at       INTEGER,                      -- unix seconds of last successful cycle
    replica_vm_id      TEXT,                         -- target VM id of the replica (after 1st cycle)
    last_error         TEXT,                         -- last cycle error (no secrets)
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_replpolicy_source ON replication_policies(source_provider_id);
CREATE INDEX IF NOT EXISTS idx_replpolicy_target ON replication_policies(target_provider_id);
