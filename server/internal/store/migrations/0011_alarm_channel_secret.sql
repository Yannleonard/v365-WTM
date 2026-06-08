-- UniHV alarms: real SMTP notification channels (travaux.md §4.5).
-- The smtp channel type stores its non-secret config (host/port/username/from/to/
-- TLS) as JSON in alarm_channels.config, and its SMTP PASSWORD sealed at rest in
-- config_secret as an AES-256-GCM BLOB (authz.SealSecret, mirroring how TOTP / SSO
-- / registry / storage-backend secrets are handled). The password is NEVER stored
-- in plaintext, NEVER returned by GET /alarms/channels, and NEVER logged.
ALTER TABLE alarm_channels ADD COLUMN config_secret BLOB;
