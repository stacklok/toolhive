#!/usr/bin/env bash
set -e

# Verify that generated Markdown docs are up-to-date.
tmpdir=$(mktemp -d)
go run cmd/help/main.go --dir "$tmpdir"
diff -Naur -I "^  date:" "$tmpdir" docs/cli/
echo "######################################################################################"
echo "If diffs are found, please run: \`task docs\` to regenerate the docs."
echo "######################################################################################"
