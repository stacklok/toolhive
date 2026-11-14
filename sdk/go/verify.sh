#!/usr/bin/env bash
# SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
# SPDX-License-Identifier: Apache-2.0
set -euo pipefail

root=$(git rev-parse --show-toplevel)
tmpdir=$(mktemp -d)
# Intentionally leave this temporary directory for the operating system to clean
# up; verification must not delete caller-visible filesystem paths.

go run "$root/cmd/help/openapi-normalize" "$root/docs/server/swagger.json" "$tmpdir/openapi.json" "$tmpdir/openapi.yaml"
diff -u "$tmpdir/openapi.json" "$root/sdk/go/openapi.json"
diff -u "$tmpdir/openapi.yaml" "$root/sdk/go/openapi.yaml"
ogen --config "$root/sdk/go/ogen.yml" --target "$tmpdir/client" --package client "$root/sdk/go/openapi.json"
go run "$root/cmd/help/ogen-client-wrapper" "$tmpdir/client/oas_client_gen.go"
addlicense -f "$root/.github/license-header.txt" $(find "$tmpdir/client" -name '*.go' -type f)
diff -ru -x defaults.go -x client_test.go "$tmpdir/client" "$root/sdk/go/client"
