-- SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
-- SPDX-License-Identifier: Apache-2.0

-- Capabilities table stores tool/resource/prompt metadata.
--
-- content_hash identifies the exact input the stored embedding was produced
-- from, covering both the embedded text and the embedding backend (see
-- embeddingCacheKey), so UpsertTools can reuse a vector when the hash still
-- matches. NULL means no embedding (FTS5-only mode).
--
-- The database is recreated in memory on every process start, so this column
-- needs no migration. That changes if the store ever becomes file-backed.
CREATE TABLE IF NOT EXISTS llm_capabilities (
    name TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    embedding BLOB,
    content_hash TEXT
);

-- The reuse lookup selects by content_hash across the whole table, not by the
-- name primary key.
CREATE INDEX IF NOT EXISTS llm_capabilities_content_hash_idx
    ON llm_capabilities (content_hash);

-- FTS5 virtual table for full-text search with BM25 ranking.
-- tokenize='porter' uses the Porter stemming algorithm so that morphological
-- variants of a word (e.g. "running", "runs", "ran") match the root form "run".
-- This improves recall for natural-language tool descriptions.
CREATE VIRTUAL TABLE IF NOT EXISTS llm_capabilities_fts USING fts5(
    name,
    description,
    content=llm_capabilities,
    content_rowid=rowid,
    tokenize='porter'
);

-- Triggers to keep FTS index in sync with llm_capabilities table
CREATE TRIGGER IF NOT EXISTS llm_capabilities_after_insert AFTER INSERT ON llm_capabilities BEGIN
    INSERT INTO llm_capabilities_fts(rowid, name, description) VALUES (new.rowid, new.name, new.description);
END;

CREATE TRIGGER IF NOT EXISTS llm_capabilities_after_delete AFTER DELETE ON llm_capabilities BEGIN
    INSERT INTO llm_capabilities_fts(llm_capabilities_fts, rowid, name, description) VALUES('delete', old.rowid, old.name, old.description);
END;

CREATE TRIGGER IF NOT EXISTS llm_capabilities_after_update AFTER UPDATE ON llm_capabilities BEGIN
    INSERT INTO llm_capabilities_fts(llm_capabilities_fts, rowid, name, description) VALUES('delete', old.rowid, old.name, old.description);
    INSERT INTO llm_capabilities_fts(rowid, name, description) VALUES (new.rowid, new.name, new.description);
END;
