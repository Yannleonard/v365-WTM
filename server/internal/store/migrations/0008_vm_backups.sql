-- UniHV scheduled VM BACKUPS (Lot 5B). Two tables:
--
--   vm_backups          one row per produced backup artifact set: a point-in-time
--                       snapshot of a VM whose disk(s) were exported (qcow2 via
--                       qemu-img) and pushed to a storage backend under a keyed
--                       path (vm/<vmId>/<timestamp>/). disks_json holds the per-disk
--                       artifact list (key + size). source_provider_id/backend_id
--                       reference the vprovider + storage_backends rows. This is the
--                       restore catalog: Restore pulls these artifacts and imports a
--                       NEW VM on a target provider.
--
--   vm_backup_policies  one row per scheduled-backup policy: back up vm_id on
--                       source_provider_id to backend_id every interval_seconds,
--                       keeping at most retention_count backups (older ones pruned).
--                       Mirrors replication_policies (durable definition + last-run
--                       summary; the in-memory engine resumes enabled policies at
--                       boot). *_at columns are unix epoch seconds (UTC).

CREATE TABLE IF NOT EXISTS vm_backups (
    id                 TEXT PRIMARY KEY,            -- UUID
    vm_id              TEXT NOT NULL,               -- source VM (provider-native id)
    vm_name            TEXT,                         -- human label captured at backup time
    provider_id        TEXT NOT NULL,               -- source vprovider id
    backend_id         TEXT NOT NULL,               -- storage_backends.id the artifacts live on
    policy_id          TEXT,                         -- owning policy (NULL for ad-hoc "Back up now")
    key_prefix         TEXT NOT NULL,               -- object key prefix, e.g. vm/<vmId>/<ts>/
    size_bytes         INTEGER NOT NULL DEFAULT 0,  -- total artifact bytes
    disk_count         INTEGER NOT NULL DEFAULT 0,
    disks_json         TEXT,                         -- JSON array of {key,sizeBytes,format}
    guest_os           TEXT,
    firmware           TEXT,
    status             TEXT NOT NULL DEFAULT 'pending', -- pending|completed|error
    error              TEXT,                         -- failure detail (no secrets)
    created_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vm_backups_vm ON vm_backups(vm_id);
CREATE INDEX IF NOT EXISTS idx_vm_backups_policy ON vm_backups(policy_id);
CREATE INDEX IF NOT EXISTS idx_vm_backups_backend ON vm_backups(backend_id);

CREATE TABLE IF NOT EXISTS vm_backup_policies (
    id                 TEXT PRIMARY KEY,            -- UUID
    name               TEXT NOT NULL,               -- human label
    provider_id        TEXT NOT NULL,               -- source vprovider id
    vm_id              TEXT NOT NULL,               -- VM to back up
    backend_id         TEXT NOT NULL,               -- target storage backend id
    interval_seconds   INTEGER NOT NULL DEFAULT 86400, -- seconds between backups
    retention_count    INTEGER NOT NULL DEFAULT 7,  -- keep at most N backups; prune older
    enabled            INTEGER NOT NULL DEFAULT 1,
    status             TEXT NOT NULL DEFAULT 'idle',-- idle|running|error
    last_run_at        INTEGER,                      -- unix seconds of last successful run
    last_error         TEXT,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vm_backup_policies_vm ON vm_backup_policies(vm_id);
