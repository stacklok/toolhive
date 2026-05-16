-- +goose Up

-- SQLite does not support ALTER COLUMN, so we recreate the table with the
-- updated CHECK constraint to include the 'episodic' memory type.

CREATE TABLE IF NOT EXISTS memory_entries_new (
    id                TEXT PRIMARY KEY,
    type              TEXT NOT NULL CHECK (type IN ('semantic','procedural','episodic')),
    content           TEXT NOT NULL,
    tags              TEXT NOT NULL DEFAULT '[]',
    author            TEXT NOT NULL CHECK (author IN ('human','agent')),
    agent_id          TEXT NOT NULL DEFAULT '',
    session_id        TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL CHECK (source IN ('memory','skill')),
    skill_ref         TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','flagged','expired','archived')),
    trust_score       REAL NOT NULL DEFAULT 0,
    staleness_score   REAL NOT NULL DEFAULT 0,
    access_count      INTEGER NOT NULL DEFAULT 0,
    last_accessed_at  TEXT,
    flagged_at        TEXT,
    flag_reason       TEXT NOT NULL DEFAULT '',
    ttl_days          INTEGER,
    expires_at        TEXT,
    archived_at       TEXT,
    consolidated_into TEXT NOT NULL DEFAULT '',
    crystallized_into TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

INSERT INTO memory_entries_new SELECT * FROM memory_entries;
DROP TABLE memory_entries;
ALTER TABLE memory_entries_new RENAME TO memory_entries;

CREATE INDEX IF NOT EXISTS idx_memory_entries_type_status
    ON memory_entries(type, status);

CREATE INDEX IF NOT EXISTS idx_memory_entries_expires_at
    ON memory_entries(expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down

-- Revert: drop episodic rows then recreate the narrower constraint.
DELETE FROM memory_entries WHERE type = 'episodic';

CREATE TABLE IF NOT EXISTS memory_entries_old (
    id                TEXT PRIMARY KEY,
    type              TEXT NOT NULL CHECK (type IN ('semantic','procedural')),
    content           TEXT NOT NULL,
    tags              TEXT NOT NULL DEFAULT '[]',
    author            TEXT NOT NULL CHECK (author IN ('human','agent')),
    agent_id          TEXT NOT NULL DEFAULT '',
    session_id        TEXT NOT NULL DEFAULT '',
    source            TEXT NOT NULL CHECK (source IN ('memory','skill')),
    skill_ref         TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active','flagged','expired','archived')),
    trust_score       REAL NOT NULL DEFAULT 0,
    staleness_score   REAL NOT NULL DEFAULT 0,
    access_count      INTEGER NOT NULL DEFAULT 0,
    last_accessed_at  TEXT,
    flagged_at        TEXT,
    flag_reason       TEXT NOT NULL DEFAULT '',
    ttl_days          INTEGER,
    expires_at        TEXT,
    archived_at       TEXT,
    consolidated_into TEXT NOT NULL DEFAULT '',
    crystallized_into TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

INSERT INTO memory_entries_old SELECT * FROM memory_entries;
DROP TABLE memory_entries;
ALTER TABLE memory_entries_old RENAME TO memory_entries;

CREATE INDEX IF NOT EXISTS idx_memory_entries_type_status
    ON memory_entries(type, status);

CREATE INDEX IF NOT EXISTS idx_memory_entries_expires_at
    ON memory_entries(expires_at) WHERE expires_at IS NOT NULL;
