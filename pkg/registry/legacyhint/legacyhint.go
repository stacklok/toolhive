// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package legacyhint detects the deprecated ToolHive registry format and
// supplies a single migration message used by every parser/validator entry
// point. Lives in its own leaf package so pkg/registry and pkg/config can both
// import it without creating an import cycle (pkg/registry imports pkg/config).
package legacyhint

import "encoding/json"

// MigrationMessage is the user-facing text returned when a legacy ToolHive
// registry file is detected. Kept identical across the runtime parser,
// set-registry-file, set-registry-url, and remote provider validation paths.
const MigrationMessage = "registry file appears to be in the legacy ToolHive format; " +
	"run `thv registry convert --in <path> --in-place` to migrate to the upstream MCP format"

// Looks reports whether the JSON document has top-level "servers",
// "remote_servers", or "groups" — the markers of the legacy ToolHive registry
// layout. The upstream format wraps these under a top-level "data" object, so a
// match here means the input is legacy (or close enough that emitting the
// migration hint is more useful than a generic decode error).
//
// Used to short-circuit with a migration hint instead of the misleading
// "no servers" error that Go's JSON decoder produces when legacy fields are
// silently dropped during unmarshal into UpstreamRegistry.
func Looks(data []byte) bool {
	var probe struct {
		Servers       json.RawMessage `json:"servers"`
		RemoteServers json.RawMessage `json:"remote_servers"`
		Groups        json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return len(probe.Servers) > 0 || len(probe.RemoteServers) > 0 || len(probe.Groups) > 0
}

// IsUpstream reports whether the JSON document appears to use the upstream
// registry format. The discriminator is a top-level "data" object — only the
// upstream format wraps servers inside it. The "$schema" key alone is not
// sufficient because the legacy format also includes one.
func IsUpstream(data []byte) bool {
	var probe struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return len(probe.Data) > 0 && probe.Data[0] == '{'
}
