-- FTS5 schema for BM25 full-text search
-- Complements chromem-go (which handles vector/semantic search)
--
-- This schema only contains:
-- 1. Metadata tables for tool/server information
-- 2. FTS5 virtual tables for BM25 keyword search
--
-- Note: chromem-go handles embeddings separately in memory/persistent storage

-- Backend servers metadata (for FTS queries and joining)
CREATE TABLE IF NOT EXISTS backend_servers_fts (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT,
    server_group TEXT NOT NULL DEFAULT 'default',
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_backend_servers_fts_group ON backend_servers_fts(server_group);

-- Backend tools metadata (for FTS queries and joining)
CREATE TABLE IF NOT EXISTS backend_tools_fts (
    id TEXT PRIMARY KEY,
    mcpserver_id TEXT NOT NULL,
    tool_name TEXT NOT NULL,
    tool_description TEXT,
    input_schema TEXT,  -- JSON string
    token_count INTEGER NOT NULL DEFAULT 0,
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (mcpserver_id) REFERENCES backend_servers_fts(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_backend_tools_fts_server ON backend_tools_fts(mcpserver_id);
CREATE INDEX IF NOT EXISTS idx_backend_tools_fts_name ON backend_tools_fts(tool_name);

-- FTS5 virtual table for backend tools
-- Uses Porter stemming for better keyword matching
-- Indexes: server name, tool name, and tool description
CREATE VIRTUAL TABLE IF NOT EXISTS backend_tool_fts_index
USING fts5(
    tool_id UNINDEXED,
    mcp_server_name,
    tool_name,
    tool_description,
    tokenize='porter',
    content='backend_tools_fts',
    content_rowid='rowid'
);

-- Triggers to keep FTS5 index in sync with backend_tools_fts table
CREATE TRIGGER IF NOT EXISTS backend_tools_fts_ai AFTER INSERT ON backend_tools_fts BEGIN
    INSERT INTO backend_tool_fts_index(
        rowid,
        tool_id, 
        mcp_server_name,
        tool_name,
        tool_description
    )
    SELECT 
        rowid,
        new.id,
        (SELECT name FROM backend_servers_fts WHERE id = new.mcpserver_id),
        new.tool_name,
        COALESCE(new.tool_description, '')
    FROM backend_tools_fts
    WHERE id = new.id;
END;

CREATE TRIGGER IF NOT EXISTS backend_tools_fts_ad AFTER DELETE ON backend_tools_fts BEGIN
    INSERT INTO backend_tool_fts_index(
        backend_tool_fts_index,
        rowid,
        tool_id,
        mcp_server_name,
        tool_name,
        tool_description
    ) VALUES (
        'delete',
        old.rowid,
        old.id,
        NULL,
        NULL,
        NULL
    );
END;

CREATE TRIGGER IF NOT EXISTS backend_tools_fts_au AFTER UPDATE ON backend_tools_fts BEGIN
    INSERT INTO backend_tool_fts_index(
        backend_tool_fts_index,
        rowid,
        tool_id,
        mcp_server_name,
        tool_name,
        tool_description
    ) VALUES (
        'delete',
        old.rowid,
        old.id,
        NULL,
        NULL,
        NULL
    );
    INSERT INTO backend_tool_fts_index(
        rowid,
        tool_id,
        mcp_server_name,
        tool_name,
        tool_description
    )
    SELECT 
        rowid,
        new.id,
        (SELECT name FROM backend_servers_fts WHERE id = new.mcpserver_id),
        new.tool_name,
        COALESCE(new.tool_description, '')
    FROM backend_tools_fts
    WHERE id = new.id;
END;
