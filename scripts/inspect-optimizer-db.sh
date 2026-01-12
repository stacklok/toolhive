#!/usr/bin/env bash
# Quick inspection script for optimizer test database

DB="/tmp/optimizer-test.db"

echo "🔍 ToolHive Optimizer Database Inspection"
echo "=========================================="
echo ""

if [ ! -f "$DB" ]; then
    echo "❌ Database not found at $DB"
    echo "   Run: task test-optimizer"
    exit 1
fi

echo "📊 Database Info:"
ls -lh "$DB"
echo ""

echo "📋 Tables:"
sqlite3 "$DB" ".tables"
echo ""

echo "🖥️  Workload Servers:"
sqlite3 "$DB" -header -column "SELECT name, status, url FROM mcpservers_workload;"
echo ""

echo "🔧 Tools:"
sqlite3 "$DB" -header -column "SELECT 
    json_extract(details, '$.name') as tool_name,
    json_extract(details, '$.description') as description,
    token_count
FROM tools_workload;"
echo ""

echo "📈 Statistics:"
sqlite3 "$DB" -header -column "SELECT 
    COUNT(*) as total_tools,
    SUM(token_count) as total_tokens,
    AVG(token_count) as avg_tokens
FROM tools_workload;"
echo ""

echo "🔢 Vector Embeddings:"
echo "   (Note: Vector tables require sqlite-vec extension to query)"
echo "   Table: workload_tool_vectors (contains 384-dim embeddings)"
echo "   Table: workload_server_vector (server-level embeddings)"
echo ""

echo "🔍 Full-Text Search Test (search for 'weather'):"
sqlite3 "$DB" -header -column "SELECT 
    tool_name,
    tool_description
FROM workload_tool_fts 
WHERE workload_tool_fts MATCH 'weather';"
echo ""

echo "💡 Interactive Commands:"
echo "   sqlite3 $DB"
echo ""
echo "   -- View tool details:"
echo "   SELECT * FROM tools_workload;"
echo ""
echo "   -- Search by text:"
echo "   SELECT * FROM workload_tool_fts WHERE workload_tool_fts MATCH 'your query';"
echo ""
echo "   -- See vector data:"
echo "   SELECT * FROM workload_tool_vectors;"

