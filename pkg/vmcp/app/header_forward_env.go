// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/headerforward/wirefmt"
)

// readHeaderForwardFromEnv reconstructs the per-backend vmcp.HeaderForwardConfig map
// by walking environment variables emitted by the operator on the vMCP pod.
//
// The operator emits one JSON-encoded manifest env var per backend named
// "TOOLHIVE_HEADER_FORWARD_<NORMALIZED_ENTRY>". The JSON value carries every
// configured header for that backend with original (un-normalized) names preserved:
//
//	{"addPlaintextHeaders":{"X-MCP-Toolsets":"projects,issues"},
//	 "addHeadersFromSecret":{"X-Api-Key":"HEADER_FORWARD_X_API_KEY_<entry>"}}
//
// AddHeadersFromSecret values are secret IDENTIFIERS, not values. Secret values
// resolve later inside resolveHeaderForward via secrets.EnvironmentProvider, which
// reads TOOLHIVE_SECRET_<identifier> env vars (delivered separately via
// valueFrom.secretKeyRef so the value never enters the operator's view of the world).
//
// The map key is the normalized entry segment from the env-var suffix — the same
// value the operator's GenerateHeaderForwardManifestEnvVarName produces for that
// backend's name. Callers look up by passing the original backend name through
// ctrlutil.NormalizeHeaderForEnvVar before indexing.
func readHeaderForwardFromEnv(envEntries []string) map[string]*vmcp.HeaderForwardConfig {
	out := make(map[string]*vmcp.HeaderForwardConfig)
	for _, entry := range envEntries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, wirefmt.ManifestEnvVarPrefix) {
			continue
		}
		ownerSegment := strings.TrimPrefix(name, wirefmt.ManifestEnvVarPrefix)

		var cfg vmcp.HeaderForwardConfig
		if err := json.Unmarshal([]byte(value), &cfg); err != nil {
			// A malformed manifest is a programmer error in the operator; log and
			// skip rather than fail the whole startup — the backend will simply have
			// no headerForward attached.
			slog.Warn("invalid headerForward manifest from env, skipping",
				"envvar", name, "error", err)
			continue
		}
		// Log a collision loudly: the second config silently overwriting the first
		// would mask a misconfiguration that is otherwise extremely hard to debug.
		if _, dup := out[ownerSegment]; dup {
			slog.Warn("duplicate headerForward manifest env var; later value overrides earlier",
				"envvar", name, "ownerSegment", ownerSegment)
		}
		out[ownerSegment] = &cfg
	}
	return out
}
