// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"encoding/json"
	"fmt"
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
// envEntries is the result of os.Environ()-style "KEY=VALUE" iteration.
// staticBackendNames is the set of backend names declared in the on-disk
// vmcp config; only manifest env vars whose decoded entry name matches one
// of them are accepted. This prevents stray env vars (e.g. from another
// component using a similar naming convention) from constructing phantom
// backends.
//
// Returns a map[backendName]*HeaderForwardConfig. Backends without any
// matching env var are absent from the map; callers reading by backend
// name should treat absence as "no header forward configured."
func readHeaderForwardFromEnv(envEntries []string, staticBackendNames []string) map[string]*vmcp.HeaderForwardConfig {
	if len(staticBackendNames) == 0 {
		return nil
	}

	canonicalByNormalized := make(map[string]string, len(staticBackendNames))
	for _, name := range staticBackendNames {
		canonicalByNormalized[normalizeForEnvSegment(name)] = name
	}

	out := make(map[string]*vmcp.HeaderForwardConfig)

	for _, entry := range envEntries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		// Only the manifest prefix carries headerForward data. The
		// per-(entry, header) secret env vars match a longer prefix
		// (TOOLHIVE_SECRET_HEADER_FORWARD_) and must be rejected here so a
		// secret env var doesn't get JSON-decoded as a manifest.
		if !strings.HasPrefix(name, ctrlutil.HeaderForwardManifestEnvVarPrefix) ||
			strings.HasPrefix(name, ctrlutil.HeaderForwardSecretEnvVarPrefix) {
			continue
		}
		ownerSegment := strings.TrimPrefix(name, ctrlutil.HeaderForwardManifestEnvVarPrefix)
		canonical, found := canonicalByNormalized[ownerSegment]
		if !found {
			continue
		}

		var cfg vmcp.HeaderForwardConfig
		if err := json.Unmarshal([]byte(value), &cfg); err != nil {
			// A malformed manifest is a programmer error in the operator;
			// log and skip rather than fail the whole startup. The backend
			// will simply have no headerForward attached.
			//
			// We use a sentinel error string so an operator e2e test could
			// grep for it if needed.
			_ = fmt.Errorf("invalid headerForward manifest for backend %q: %w", canonical, err)
			continue
		}
		out[canonical] = &cfg
	}

	return out
}

// normalizeForEnvSegment applies the same normalization as
// ctrlutil.GenerateHeaderForwardSecretEnvVarName /
// GenerateHeaderForwardManifestEnvVarName so the reverse lookup matches
// names the operator emitted. Kept private here (and a copy in ctrlutil)
// because importing ctrlutil's private regexp would expand its surface
// unnecessarily.
func normalizeForEnvSegment(s string) string {
	upper := strings.ToUpper(strings.ReplaceAll(s, "-", "_"))
	out := make([]byte, 0, len(upper))
	for i := 0; i < len(upper); i++ {
		c := upper[i]
		if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
