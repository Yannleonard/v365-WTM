-- UniHV hypervisor connections (ADR-UNIHV-002, D-007).
-- One row per registered hypervisor endpoint (standalone host OR cluster). On
-- startup / on create, the server instantiates the matching LIVE provider
-- (KVM/libvirt, Hyper-V/WMI, ESXi/govmomi, Xen/XAPI) and registers it in the
-- vprovider.Registry. Credentials are sealed with AES-256-GCM (authz.SealSecret),
-- never stored or returned in plaintext. *_at columns are unix epoch seconds (UTC).

CREATE TABLE IF NOT EXISTS hypervisor_connections (
    id            TEXT PRIMARY KEY,            -- UUID; also the vprovider provider id
    name          TEXT NOT NULL,               -- human label
    kind          TEXT NOT NULL,               -- kvm | hyperv | vmware | xen
    endpoint      TEXT NOT NULL,               -- libvirt URI/socket, vCenter/ESXi URL, XAPI URL, or "" for local Hyper-V
    username      TEXT,                         -- for vmware/xen (libvirt/hyperv local may omit)
    secret_enc    BLOB,                         -- sealed password/credential (AES-256-GCM); never plaintext
    insecure_tls  INTEGER NOT NULL DEFAULT 0,  -- skip TLS verify (lab vCenter/XAPI with self-signed)
    enabled       INTEGER NOT NULL DEFAULT 1,  -- whether to connect on startup
    status        TEXT NOT NULL DEFAULT 'pending', -- pending|connected|error
    last_error    TEXT,                         -- last connection error (no secrets)
    last_seen_at  INTEGER,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    UNIQUE(kind, endpoint, username)
);
CREATE INDEX IF NOT EXISTS idx_hvconn_kind ON hypervisor_connections(kind);
