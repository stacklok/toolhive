-- +goose Up

CREATE TABLE installed_plugins (
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
    license      TEXT NOT NULL DEFAULT '',
    keywords     BLOB DEFAULT NULL,          -- JSONB-encoded []string
    client_apps  BLOB DEFAULT NULL,          -- JSONB-encoded []string
    components   BLOB DEFAULT NULL,          -- JSONB-encoded map[string]int
    signature    TEXT DEFAULT NULL,
    status       TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('installed', 'pending', 'failed')),
    installed_at TEXT NOT NULL
                      DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    UNIQUE (entry_id, scope, project_root)
);

CREATE TABLE plugin_dependencies (
    installed_plugin_id INTEGER NOT NULL
                              REFERENCES installed_plugins(id) ON DELETE CASCADE,
    dep_name            TEXT NOT NULL DEFAULT '',
    dep_reference       TEXT NOT NULL,
    dep_digest          TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (installed_plugin_id, dep_reference)
);

-- +goose Down

DROP TABLE IF EXISTS plugin_dependencies;
DROP TABLE IF EXISTS installed_plugins;
