-- UniHV pluggable storage backends (Storage Engineer deliverable).
-- One row per registered storage backend the operator can attach for VM images,
-- ISO libraries and backups. Two families:
--   SAN/NAS (nfs|iscsi|smb) — realized as a libvirt storage pool of that type on a
--     target KVM provider (pool type netfs/iscsi/...); host+target/share identify it.
--   cloud object store (azureblob|s3) — accessed via a minimal stdlib REST client
--     (Azure SharedKey / AWS SigV4) for images/ISO/backups; bucket/container + region.
-- Credentials (NFS none; SMB password; Azure account key; S3 secret access key) are
-- sealed with AES-256-GCM (authz.SealSecret) and NEVER stored/returned in plaintext.
-- *_at columns are unix epoch seconds (UTC). Mirrors hypervisor_connections.

CREATE TABLE IF NOT EXISTS storage_backends (
    id            TEXT PRIMARY KEY,            -- UUID
    name          TEXT NOT NULL,               -- human label
    type          TEXT NOT NULL,               -- nfs | iscsi | smb | azureblob | s3
    endpoint      TEXT,                         -- host/server (NFS/iSCSI/SMB) or service URL (azure/s3 optional override)
    target        TEXT,                         -- NFS export path / iSCSI IQN / SMB UNC / S3 bucket / Azure container
    username      TEXT,                         -- SMB user, Azure account name, S3 access key id
    secret_enc    BLOB,                         -- sealed secret (SMB pass / Azure account key / S3 secret key); never plaintext
    region        TEXT,                         -- S3 region (e.g. us-east-1); blank for others
    provider_id   TEXT,                         -- target KVM provider id (vprovider) for nfs/iscsi/smb pool define
    options       TEXT,                         -- free-form JSON options (e.g. {"poolName":"...","prefix":"..."})
    enabled       INTEGER NOT NULL DEFAULT 1,
    status        TEXT NOT NULL DEFAULT 'pending', -- pending|connected|error
    last_error    TEXT,                         -- last test/connect error (no secrets)
    last_seen_at  INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE(type, endpoint, target)
);
CREATE INDEX IF NOT EXISTS idx_storage_backend_type ON storage_backends(type);
