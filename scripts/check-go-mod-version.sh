#!/usr/bin/env bash
# Fail if any go.mod file specifies a patch version in the go directive.
# Use 'go 1.X' not 'go 1.X.Y' — patch versions are irrelevant to module
# compatibility and create unnecessary churn when toolchains update.

set -euo pipefail

found=$(grep -rE '^go [0-9]+\.[0-9]+\.[0-9]+' --include='go.mod' . || true)
if [ -n "$found" ]; then
  echo "ERROR: patch version found in go directive (use 'go 1.X' not 'go 1.X.Y'):"
  echo "$found"
  exit 1
fi
