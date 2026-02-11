-- +goose Up

CREATE TABLE entries (
    id          INTEGER PRIMARY KEY,
    entry_type  TEXT NOT NULL,
    name        TEXT NOT NULL,
    created_at  TEXT NOT NULL
                    DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    -- updated_at is set on INSERT via DEFAULT; the application layer is
    -- responsible for setting it explicitly in UPDATE statements.
    updated_at  TEXT NOT NULL
                    DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (entry_type, name)
);

CREATE TABLE installed_skills (
    id           INTEGER PRIMARY KEY,
    entry_id     INTEGER NOT NULL REFERENCES entries(id) ON DELETE CASCADE,
    scope        TEXT NOT NULL DEFAULT 'user'
                      CHECK (scope IN ('user', 'project')),
    project_root TEXT NOT NULL DEFAULT '',
    reference    TEXT NOT NULL DEFAULT '',
    tag          TEXT NOT NULL DEFAULT '',
    digest       TEXT NOT NULL DEFAULT '',
    version      TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    author       TEXT NOT NULL DEFAULT '',
    tags         BLOB DEFAULT NULL,          -- JSONB-encoded []string
    client_apps  BLOB DEFAULT NULL,          -- JSONB-encoded []string
    status       TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('installed', 'pending', 'failed')),
    installed_at TEXT NOT NULL
                      DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (entry_id, scope, project_root)
);

CREATE TABLE skill_dependencies (
    installed_skill_id INTEGER NOT NULL
                           REFERENCES installed_skills(id) ON DELETE CASCADE,
    dep_name           TEXT NOT NULL DEFAULT '',
    dep_reference      TEXT NOT NULL,
    dep_digest         TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (installed_skill_id, dep_reference)
);

CREATE TABLE oci_tags (
    reference TEXT NOT NULL,
    digest    TEXT NOT NULL,
    PRIMARY KEY (reference)
);

-- +goose Down

DROP TABLE IF EXISTS oci_tags;
DROP TABLE IF EXISTS skill_dependencies;
DROP TABLE IF EXISTS installed_skills;
DROP TABLE IF EXISTS entries;
