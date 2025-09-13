#!/usr/bin/env bash
set -e

# Verify that generated CLI docs are up-to-date.
tmpdir=$(mktemp -d)
go run cmd/help/main.go --dir "$tmpdir"
diff -Naur -I "^  date:" "$tmpdir" docs/cli/

# Generate API docs in temp directory that mimics the final structure
api_tmpdir=$(mktemp -d)
mkdir -p "$api_tmpdir/server"
swag init -g pkg/api/server.go --v3.1 -o "$api_tmpdir/server" --exclude ./cmd/thv-registry-api
# Exclude README.md from diff as it's manually maintained
diff -Naur --exclude="README.md" "$api_tmpdir/server" docs/server/

echo "######################################################################################"
echo "If diffs are found, please run: \`task docs\` to regenerate the docs."
echo "######################################################################################"
