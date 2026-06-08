-- UniHV vSphere-style ALARMS (threshold-driven, stateful health rules over the
-- unified inventory + metrics). Three tables:
--   alarm_definitions : user-defined rules (target/metric/comparator/threshold/
--                       duration/severity/enabled + notify channel ids as JSON).
--   alarm_channels    : notification destinations (webhook URL / email-stub addr).
--   alarm_instances   : the durable set of currently-ACTIVE alarm instances so an
--                       in-flight alarm survives a restart (the engine holds the
--                       live runtime state; this is the resume snapshot).
-- *_at columns are unix epoch seconds (UTC). raised_at on alarm_instances is the
-- moment the breach duration elapsed and the alarm was raised.

CREATE TABLE IF NOT EXISTS alarm_definitions (
    id                 TEXT PRIMARY KEY,            -- UUID
    name               TEXT NOT NULL,               -- human label
    target             TEXT NOT NULL,               -- vm|host|datastore
    metric             TEXT NOT NULL,               -- cpu|memory|disk|storage_pct|state
    comparator         TEXT NOT NULL DEFAULT 'gt',  -- gt|lt|eq
    threshold          REAL NOT NULL DEFAULT 0,     -- numeric bound (percent / bytes)
    state_value        TEXT,                         -- compared state for metric=state
    duration_sec       INTEGER NOT NULL DEFAULT 0,  -- breach must persist this long before raise
    severity           TEXT NOT NULL DEFAULT 'warning', -- info|warning|critical
    enabled            INTEGER NOT NULL DEFAULT 1,
    notify_channel_ids TEXT NOT NULL DEFAULT '[]',  -- JSON array of alarm_channels.id
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_alarmdef_enabled ON alarm_definitions(enabled);

CREATE TABLE IF NOT EXISTS alarm_channels (
    id         TEXT PRIMARY KEY,            -- UUID
    name       TEXT NOT NULL,               -- human label
    type       TEXT NOT NULL,               -- webhook|email-stub
    config     TEXT NOT NULL DEFAULT '',    -- webhook URL or email address
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS alarm_instances (
    id              TEXT PRIMARY KEY,        -- definitionId:objectId
    definition_id   TEXT NOT NULL,
    definition_name TEXT NOT NULL,
    object_id       TEXT NOT NULL,
    object_name     TEXT NOT NULL,
    object_type     TEXT NOT NULL,           -- vm|host|datastore
    severity        TEXT NOT NULL,
    metric          TEXT NOT NULL,
    value           REAL NOT NULL DEFAULT 0,
    state_raw       TEXT,
    raised_at       INTEGER NOT NULL,
    last_notified_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_alarminst_def ON alarm_instances(definition_id);
