// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"strings"

	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// readHeaderForwardFromEnv reconstructs the per-backend
// vmcp.HeaderForwardConfig map by walking environment variables emitted by
// the operator on the vMCP pod. Two prefixes are honored:
//
//   - HeaderForwardPlaintextEnvVarPrefix carries literal header values, one
//     env var per (entry, header) pair, named
//     "TOOLHIVE_HEADER_PLAINTEXT_<NORMALIZED_HEADER>_<NORMALIZED_ENTRY>".
//   - HeaderForwardSecretEnvVarPrefix carries secret-backed entries via
//     valueFrom.secretKeyRef, one env var per (entry, header) pair, named
//     "TOOLHIVE_SECRET_HEADER_FORWARD_<NORMALIZED_HEADER>_<NORMALIZED_ENTRY>".
//     Only the IDENTIFIER (the suffix after TOOLHIVE_SECRET_) is captured here;
//     the secret value resolves later inside resolveHeaderForward via
//     secrets.EnvironmentProvider, which reads the same env var by name.
//
// envEntries is the result of os.Environ()-style "KEY=VALUE" iteration.
// staticBackendNames is the set of backend names declared in the on-disk
// vmcp config; only env vars whose decoded entry name matches one of them
// are accepted. This prevents stray env vars (e.g. from another component
// using a similar naming convention) from constructing phantom backends.
//
// Returns a map[backendName]*HeaderForwardConfig. Backends without any
// matching env var are absent from the map; callers reading by backend name
// should treat absence as "no header forward configured."
//
// Header-name reconstruction: the operator-side normalizer is one-way
// (uppercases, replaces "-" and other non-[A-Z0-9_] characters with "_").
// The runtime cannot recover the original casing or punctuation, so the
// reconstructed header keys are emitted in the normalized form. HTTP header
// matching is canonical-case-insensitive, so this is safe for the round
// tripper's eventual http.Header.Set call (which canonicalizes regardless).
func readHeaderForwardFromEnv(envEntries []string, staticBackendNames []string) map[string]*vmcp.HeaderForwardConfig {
	if len(staticBackendNames) == 0 {
		return nil
	}

	// Build a lookup from normalized backend name back to canonical name.
	// We only accept env vars whose decoded entry segment matches one of
	// these — anything else is dropped.
	canonicalByNormalized := make(map[string]string, len(staticBackendNames))
	for _, name := range staticBackendNames {
		canonicalByNormalized[normalizeForEnvSegment(name)] = name
	}

	out := make(map[string]*vmcp.HeaderForwardConfig)

	ensure := func(backendName string) *vmcp.HeaderForwardConfig {
		if cfg, ok := out[backendName]; ok {
			return cfg
		}
		cfg := &vmcp.HeaderForwardConfig{}
		out[backendName] = cfg
		return cfg
	}

	for _, entry := range envEntries {
		name, value, ok := splitEnv(entry)
		if !ok {
			continue
		}

		switch {
		case strings.HasPrefix(name, ctrlutil.HeaderForwardPlaintextEnvVarPrefix):
			suffix := strings.TrimPrefix(name, ctrlutil.HeaderForwardPlaintextEnvVarPrefix)
			header, backendNorm, ok := splitHeaderEntrySuffix(suffix, canonicalByNormalized)
			if !ok {
				continue
			}
			cfg := ensure(canonicalByNormalized[backendNorm])
			if cfg.AddPlaintextHeaders == nil {
				cfg.AddPlaintextHeaders = make(map[string]string)
			}
			cfg.AddPlaintextHeaders[header] = value

		case strings.HasPrefix(name, ctrlutil.HeaderForwardSecretEnvVarPrefix):
			// The secret env var name is TOOLHIVE_SECRET_HEADER_FORWARD_<H>_<E>;
			// the identifier (what secrets.EnvironmentProvider expects) is
			// HEADER_FORWARD_<H>_<E>. Strip the TOOLHIVE_SECRET_ prefix, NOT
			// the full HeaderForwardSecretEnvVarPrefix.
			identifier := strings.TrimPrefix(name, "TOOLHIVE_SECRET_")
			suffix := strings.TrimPrefix(identifier, "HEADER_FORWARD_")
			header, backendNorm, ok := splitHeaderEntrySuffix(suffix, canonicalByNormalized)
			if !ok {
				continue
			}
			cfg := ensure(canonicalByNormalized[backendNorm])
			if cfg.AddHeadersFromSecret == nil {
				cfg.AddHeadersFromSecret = make(map[string]string)
			}
			cfg.AddHeadersFromSecret[header] = identifier
		}
	}

	return out
}

// splitEnv parses a "KEY=VALUE" entry from os.Environ().
func splitEnv(entry string) (name, value string, ok bool) {
	eq := strings.IndexByte(entry, '=')
	if eq < 0 {
		return "", "", false
	}
	return entry[:eq], entry[eq+1:], true
}

// splitHeaderEntrySuffix splits "<NORMALIZED_HEADER>_<NORMALIZED_ENTRY>" into
// its two halves, knowing that <NORMALIZED_ENTRY> must be one of the static
// backend names. Header names can themselves contain underscores, so we walk
// the underscore positions from the right and pick the first split where the
// trailing segment matches a known backend.
//
// canonicalByNormalized maps normalized backend names (uppercase, "-" → "_",
// non-[A-Z0-9_] → "_") to their canonical form.
func splitHeaderEntrySuffix(suffix string, canonicalByNormalized map[string]string) (header, backendNorm string, ok bool) {
	for i := len(suffix); i > 0; i-- {
		if i < len(suffix) && suffix[i] != '_' {
			continue
		}
		head := suffix[:i]
		tail := suffix
		if i < len(suffix) {
			tail = suffix[i+1:]
		}
		if _, found := canonicalByNormalized[tail]; found && head != "" {
			return head, tail, true
		}
	}
	return "", "", false
}

// normalizeForEnvSegment applies the same normalization as
// ctrlutil.GenerateHeaderForwardSecretEnvVarName / GenerateHeaderForwardPlaintextEnvVarName
// so the reverse lookup matches names the operator emitted. Kept private
// here (and a copy in ctrlutil) because importing ctrlutil's private
// regexp would expand its surface unnecessarily.
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
