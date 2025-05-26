#!/usr/bin/env bash
set -e

# Verify that generated Markdown docs are up-to-date.
tmpdir=$(mktemp -d)
go run cmd/help/main.go --dir "$tmpdir"
echo "###########################################"
echo "If diffs are found, run: \`task docs\`"
echo "###########################################"
diff -Naur "$tmpdir" docs/cli/
