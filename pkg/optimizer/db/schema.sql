-- Initial schema for optimizer database
-- Matches the Python mcp-optimizer schema

-- Create mcpservers_registry table
CREATE TABLE IF NOT EXISTS mcpservers_registry (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    url TEXT,
    package TEXT,
    remote INTEGER NOT NULL,
    transport TEXT NOT NULL,
    description TEXT,
    server_embedding BLOB,
    "group" TEXT NOT NULL DEFAULT 'default',
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK ((remote = 1 AND url IS NOT NULL) OR (remote = 0 AND package IS NOT NULL))
);

-- Create unique partial indexes for registry servers
CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_url ON mcpservers_registry(url) WHERE remote = 1;
CREATE UNIQUE INDEX IF NOT EXISTS idx_registry_package ON mcpservers_registry(package) WHERE remote = 0;
CREATE INDEX IF NOT EXISTS idx_registry_remote ON mcpservers_registry(remote);

-- Create mcpservers_backend table
CREATE TABLE IF NOT EXISTS mcpservers_backend (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    url TEXT NOT NULL,
    backend_identifier TEXT NOT NULL,
    remote INTEGER NOT NULL,
    transport TEXT NOT NULL,
    status TEXT NOT NULL,
    registry_server_id TEXT,
    registry_server_name TEXT,
    description TEXT,
    server_embedding BLOB,
    "group" TEXT NOT NULL DEFAULT 'default',
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (registry_server_id)
        REFERENCES mcpservers_registry(id) ON DELETE SET NULL
);

-- Create indexes for backend servers
CREATE INDEX IF NOT EXISTS idx_backend_registry_id ON mcpservers_backend(registry_server_id);
CREATE INDEX IF NOT EXISTS idx_backend_remote ON mcpservers_backend(remote);
CREATE INDEX IF NOT EXISTS idx_backend_status ON mcpservers_backend(status);

-- Create tools_registry table
CREATE TABLE IF NOT EXISTS tools_registry (
    id TEXT PRIMARY KEY,
    mcpserver_id TEXT NOT NULL,
    details TEXT NOT NULL,
    details_embedding BLOB,
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (mcpserver_id) REFERENCES mcpservers_registry(id) ON DELETE CASCADE
);

-- Create index for registry tools
CREATE INDEX IF NOT EXISTS idx_tools_registry_server ON tools_registry(mcpserver_id);

-- Create tools_backend table
CREATE TABLE IF NOT EXISTS tools_backend (
    id TEXT PRIMARY KEY,
    mcpserver_id TEXT NOT NULL,
    details TEXT NOT NULL,
    details_embedding BLOB,
    token_count INTEGER NOT NULL DEFAULT 0,
    last_updated TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (mcpserver_id) REFERENCES mcpservers_backend(id) ON DELETE CASCADE
);

-- Create index for backend tools
CREATE INDEX IF NOT EXISTS idx_tools_backend_server ON tools_backend(mcpserver_id);

-- Create virtual tables for registry (sqlite-vec and FTS5)
-- Note: vec0 uses cosine distance by default
-- Embedding dimension is 384 to match BAAI/bge-small-en-v1.5 model
CREATE VIRTUAL TABLE IF NOT EXISTS registry_server_vector
USING vec0(
    server_id TEXT PRIMARY KEY,
    embedding FLOAT[384] distance_metric=cosine
);

CREATE VIRTUAL TABLE IF NOT EXISTS registry_tool_vectors
USING vec0(
    tool_id TEXT PRIMARY KEY,
    embedding FLOAT[384] distance_metric=cosine
);

CREATE VIRTUAL TABLE IF NOT EXISTS registry_tool_fts
USING fts5(
    tool_id UNINDEXED,
    mcp_server_name,
    tool_name,
    tool_description,
    tokenize='porter'
);

-- Create virtual tables for backend (sqlite-vec and FTS5)
CREATE VIRTUAL TABLE IF NOT EXISTS backend_server_vector
USING vec0(
    server_id TEXT PRIMARY KEY,
    embedding FLOAT[384] distance_metric=cosine
);

CREATE VIRTUAL TABLE IF NOT EXISTS backend_tool_vectors
USING vec0(
    tool_id TEXT PRIMARY KEY,
    embedding FLOAT[384] distance_metric=cosine
);

CREATE VIRTUAL TABLE IF NOT EXISTS backend_tool_fts
USING fts5(
    tool_id UNINDEXED,
    mcp_server_name,
    tool_name,
    tool_description,
    tokenize='porter'
);


