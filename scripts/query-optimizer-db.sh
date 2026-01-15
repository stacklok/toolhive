#!/usr/bin/env bash
#
# Query optimizer database with sqlite-vec support
#

DB="${1:-/tmp/optimizer-test.db}"
SQLITE_VEC_PATH="${SQLITE_VEC_PATH:-/tmp/sqlite-vec/vec0.dylib}"

if [ ! -f "$DB" ]; then
    echo "❌ Database not found: $DB"
    echo "Usage: $0 [path/to/database.db]"
    exit 1
fi

if [ ! -f "$SQLITE_VEC_PATH" ]; then
    echo "❌ sqlite-vec extension not found at: $SQLITE_VEC_PATH"
    echo "Run: task test-optimizer (to download it)"
    exit 1
fi

echo "🔍 Opening $DB with sqlite-vec support..."
echo ""
echo "sqlite-vec extension: $SQLITE_VEC_PATH"
echo ""
echo "Useful queries:"
echo "  .tables                                    -- List all tables"
echo "  SELECT * FROM tools_workload;              -- View tools"
echo "  SELECT * FROM workload_tool_vectors;       -- View embeddings"
echo "  SELECT * FROM mcpservers_workload;         -- View servers"
echo ""
echo "Vector search example:"
echo "  SELECT tool_id, distance FROM workload_tool_vectors"
echo "    WHERE embedding MATCH vec_f32(?);"
echo ""

# Load extension and open interactive shell
sqlite3 "$DB" <<EOF
.load $SQLITE_VEC_PATH
-- Now you can query vec0 tables
.mode column
.headers on
EOF

# Open interactive mode with extension loaded
exec sqlite3 "$DB" -cmd ".load $SQLITE_VEC_PATH" -cmd ".mode column" -cmd ".headers on"

