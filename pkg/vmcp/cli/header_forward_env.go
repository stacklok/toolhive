// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"log/slog"
	"strings"

	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// readHeaderForwardFromEnv reconstructs the per-backend
// vmcp.HeaderForwardConfig map by walking environment variables emitted by
// the operator on the vMCP pod.
//
// The operator emits one JSON-encoded manifest env var per backend named
// "TOOLHIVE_HEADER_FORWARD_<NORMALIZED_ENTRY>". The JSON value carries
// every configured header for that backend with original (un-normalized)
// names preserved:
//
//	{
//	  "addPlaintextHeaders": {"X-MCP-Toolsets":"projects,issues"},
//	  "addHeadersFromSecret": {"X-Api-Key":"HEADER_FORWARD_X_API_KEY_<entry>"}
//	}
//
// AddHeadersFromSecret values are secret IDENTIFIERS, not values. Secret
// values resolve later inside resolveHeaderForward via
// secrets.EnvironmentProvider, which reads TOOLHIVE_SECRET_<identifier>
// env vars (delivered separately via valueFrom.secretKeyRef so the value
// never enters the operator's view of the world).
//
// The map key is the normalized entry segment from the env-var suffix —
// the SAME value the operator's GenerateHeaderForwardManifestEnvVarName
// produces for that backend's name. Callers look up by passing the
// original backend name through ctrlutil.NormalizeHeaderForEnvVar before
// indexing.
func readHeaderForwardFromEnv(envEntries []string) map[string]*vmcp.HeaderForwardConfig {
	out := make(map[string]*vmcp.HeaderForwardConfig)
	for _, entry := range envEntries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(name, ctrlutil.HeaderForwardManifestEnvVarPrefix) {
			continue
		}
		ownerSegment := strings.TrimPrefix(name, ctrlutil.HeaderForwardManifestEnvVarPrefix)

		var cfg vmcp.HeaderForwardConfig
		if err := json.Unmarshal([]byte(value), &cfg); err != nil {
			// A malformed manifest is a programmer error in the operator;
			// log and skip rather than fail the whole startup. The backend
			// will simply have no headerForward attached.
			slog.Warn("invalid headerForward manifest from env, skipping",
				"envvar", name, "error", err)
			continue
		}
		out[ownerSegment] = &cfg
	}
	return out
}
