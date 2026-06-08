-- UniHV scoped API tokens (Lot 4B).
-- One row per personal access token. The RAW token is shown to the user EXACTLY
-- ONCE at creation and never persisted; only its SHA-256 hash (hex) is stored, so
-- a DB leak cannot yield a usable bearer credential. Permissions is a JSON array
-- of permission strings that MUST be a subset of the owning user's effective
-- grants — the bearer-auth path authorizes the request against this scoped set
-- intersected with the user's roles, so a token can never exceed its owner.
-- *_at columns are unix epoch seconds (UTC); NULL expires_at means non-expiring.

CREATE TABLE IF NOT EXISTS api_tokens (
    id           TEXT PRIMARY KEY,            -- UUID
    name         TEXT NOT NULL,               -- human label (e.g. "ci-pipeline")
    user_id      TEXT NOT NULL,               -- owning user (FK users.id)
    token_hash   TEXT NOT NULL UNIQUE,        -- hex SHA-256 of the raw token; lookup key
    permissions  TEXT NOT NULL DEFAULT '[]',  -- JSON array of scoped permission strings (subset of owner's)
    expires_at   INTEGER,                     -- NULL = never expires
    last_used_at INTEGER,                     -- updated on each successful bearer auth (best-effort)
    revoked_at   INTEGER,                     -- NULL = active
    created_at   INTEGER NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_api_tokens_user ON api_tokens(user_id);
CREATE INDEX IF NOT EXISTS idx_api_tokens_hash ON api_tokens(token_hash);
