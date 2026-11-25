// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package wirefmt is the shared wire-format contract for headerForward
// data shipped from the operator to the vMCP runtime via pod env vars.
//
// Both the operator (producer) and the vMCP runtime (consumer) import this
// package: the operator emits TOOLHIVE_HEADER_FORWARD_<NORMALIZED_OWNER>
// env vars and the runtime parses them at startup. Living in pkg/ rather
// than under cmd/thv-operator preserves one-way Go layering — the
// operator binary depends on pkg/ helpers, never the reverse.
package wirefmt

import (
	"fmt"
	"regexp"
	"strings"
)

// ManifestEnvVarPrefix is the prefix the operator uses when emitting one
// JSON-encoded manifest env var per backend on a workload pod for
// MCPServerEntry / MCPRemoteProxy headerForward configuration.
//
// The full env var name is "TOOLHIVE_HEADER_FORWARD_<NORMALIZED_OWNER>";
// the value is JSON-encoded vmcp.HeaderForwardConfig with original header
// names preserved (uppercased/sanitized normalization can't recover hyphens
// or original casing on the receive side, so we carry the literal names in
// the JSON value instead). Plaintext header values appear inline; secret-
// backed entries carry only the secret identifier — the actual Secret
// value rides a sibling TOOLHIVE_SECRET_HEADER_FORWARD_<H>_<OWNER> env var
// emitted via valueFrom.secretKeyRef and resolved at request time by
// secrets.EnvironmentProvider.
const ManifestEnvVarPrefix = "TOOLHIVE_HEADER_FORWARD_"

// envVarSanitizer matches any character outside the env-var-safe set
// [A-Z0-9_]. Used by NormalizeForEnvVar to collapse non-conforming chars.
var envVarSanitizer = regexp.MustCompile(`[^A-Z0-9_]`)

// NormalizeForEnvVar applies the canonical sanitization used in every
// header-forward env-var name: uppercase, hyphens to underscores, anything
// outside [A-Z0-9_] also to underscore. The secret-ref and the manifest
// emitters MUST share this function so a header that round-trips through
// one branch never collides with the other.
//
// Collision domain: this normalization is one-way and lossy. Two distinct
// header names that differ only in non-[A-Z0-9_-] characters will collapse
// to the same normalized form (e.g. "X-A.B" and "X-A_B" both become
// "X_A_B"). For backend (owner) names this is mitigated by Kubernetes
// DNS-1123 subdomain validation, which forbids underscores in resource
// names — two K8s resources cannot present a colliding pair. Header names
// don't have that guard, so callers iterating env vars should log when
// they see the same normalized owner key twice; readers that decode a
// single-backend manifest are not affected because the JSON value carries
// the literal user-authored header names verbatim.
func NormalizeForEnvVar(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	return envVarSanitizer.ReplaceAllString(upper, "_")
}

// SecretEnvVarName generates the environment variable name for a header
// forward secret. The generated name follows the TOOLHIVE_SECRET_<identifier>
// pattern expected by secrets.EnvironmentProvider.
//
// Parameters:
//   - ownerName: the resource owning the header forwarding configuration
//     (an MCPRemoteProxy or an MCPServerEntry).
//   - headerName: the HTTP header name (e.g. "X-API-Key").
//
// Returns the full env var name (e.g. "TOOLHIVE_SECRET_HEADER_FORWARD_X_API_KEY_MY_OWNER")
// and the secret identifier (the env var name with the leading
// "TOOLHIVE_SECRET_" stripped, used by secrets.EnvironmentProvider lookups).
func SecretEnvVarName(ownerName, headerName string) (envVarName, secretIdentifier string) {
	sanitizedHeader := NormalizeForEnvVar(headerName)
	sanitizedOwner := NormalizeForEnvVar(ownerName)
	secretIdentifier = fmt.Sprintf("HEADER_FORWARD_%s_%s", sanitizedHeader, sanitizedOwner)
	envVarName = fmt.Sprintf("TOOLHIVE_SECRET_%s", secretIdentifier)
	return envVarName, secretIdentifier
}

// ManifestEnvVarName generates the environment variable name for the
// JSON-encoded headerForward manifest emitted per backend on a workload
// pod. One env var per backend; the JSON value carries every configured
// header (plaintext values inline, secret entries by identifier).
//
// Returns the full env var name (e.g. "TOOLHIVE_HEADER_FORWARD_MY_OWNER")
// and the normalized owner segment ("MY_OWNER") used when iterating env
// vars matching ManifestEnvVarPrefix.
func ManifestEnvVarName(ownerName string) (envVarName, normalizedOwner string) {
	normalizedOwner = NormalizeForEnvVar(ownerName)
	envVarName = ManifestEnvVarPrefix + normalizedOwner
	return envVarName, normalizedOwner
}
