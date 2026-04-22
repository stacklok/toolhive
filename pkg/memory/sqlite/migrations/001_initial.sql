-- +goose Up

CREATE TABLE IF NOT EXISTS memory_entries (
    id                TEXT PRIMARY KEY,
    type              TEXT NOT NULL CHECK (type IN ('semantic','procedural')),
    content           TEXT NOT NULL,
    tags              TEXT NOT NULL DEFAULT '[]',   -- JSON array
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

CREATE TABLE IF NOT EXISTS memory_revisions (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    entry_id        TEXT NOT NULL REFERENCES memory_entries(id) ON DELETE CASCADE,
    content         TEXT NOT NULL,
    author          TEXT NOT NULL,
    correction_note TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL
);

-- Embeddings stored as a JSON array of float32 values.
-- Queries load all vectors for a type+status combination and compute
-- cosine similarity in Go. Switch to an external VectorStore provider
-- for datasets > 100K entries.
CREATE TABLE IF NOT EXISTS memory_embeddings (
    entry_id   TEXT PRIMARY KEY REFERENCES memory_entries(id) ON DELETE CASCADE,
    embedding  TEXT NOT NULL  -- JSON []float32
);

CREATE INDEX IF NOT EXISTS idx_memory_entries_type_status
    ON memory_entries(type, status);

CREATE INDEX IF NOT EXISTS idx_memory_entries_expires_at
    ON memory_entries(expires_at) WHERE expires_at IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_memory_entries_expires_at;
DROP INDEX IF EXISTS idx_memory_entries_type_status;
DROP TABLE IF EXISTS memory_embeddings;
DROP TABLE IF EXISTS memory_revisions;
DROP TABLE IF EXISTS memory_entries;
