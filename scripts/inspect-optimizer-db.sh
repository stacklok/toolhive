#!/bin/bash
# Inspect the optimizer SQLite FTS5 database

set -e

DB_PATH="${1:-/tmp/vmcp-optimizer-fts.db}"

if [ ! -f "$DB_PATH" ]; then
    echo "Error: Database not found at $DB_PATH"
    echo "Usage: $0 [path-to-db]"
    exit 1
fi

echo "📊 Optimizer FTS5 Database: $DB_PATH"
echo ""

echo "📈 Statistics:"
sqlite3 "$DB_PATH" <<EOF
.mode column
.headers on
SELECT 'Servers' as Type, COUNT(*) as Count FROM backend_servers_fts
UNION ALL
SELECT 'Tools', COUNT(*) FROM backend_tools_fts;
EOF

echo ""
echo "🖥️  Servers:"
sqlite3 "$DB_PATH" <<EOF
.mode column
.headers on
SELECT id, name, server_group FROM backend_servers_fts;
EOF

echo ""
echo "🔧 Tools (first 10):"
sqlite3 "$DB_PATH" <<EOF
.mode column
.headers on
SELECT 
    SUBSTR(tool_name, 1, 30) as tool_name,
    SUBSTR(tool_description, 1, 50) as description,
    token_count
FROM backend_tools_fts 
LIMIT 10;
EOF

echo ""
echo "💡 Tools per server:"
sqlite3 "$DB_PATH" <<EOF
.mode column
.headers on
SELECT 
    s.name as server,
    COUNT(t.id) as tool_count
FROM backend_servers_fts s
LEFT JOIN backend_tools_fts t ON s.id = t.mcpserver_id
GROUP BY s.name
ORDER BY tool_count DESC;
EOF

echo ""
echo "📝 Example FTS5 search query:"
echo "  sqlite3 $DB_PATH \"SELECT tool_name, rank FROM backend_tool_fts_index WHERE backend_tool_fts_index MATCH 'repository search' ORDER BY rank LIMIT 5;\""
