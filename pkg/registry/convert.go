// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive-core/registry/converters"
	types "github.com/stacklok/toolhive-core/registry/types"
)

// ErrAlreadyUpstream indicates the input was already in upstream MCP registry
// format, so no conversion was performed.
var ErrAlreadyUpstream = errors.New("input is already in upstream format")

// ConvertJSON converts a legacy ToolHive registry JSON document into the
// upstream MCP registry format. ToolHive-specific fields are carried through to
// the publisher-provided extension block on each server. The output is
// validated against the upstream registry schema before being returned, so
// callers writing to disk get either a schema-compliant file or an error.
//
// Returns ErrAlreadyUpstream if the input is already in the upstream format.
func ConvertJSON(input []byte) ([]byte, error) {
	if isUpstreamJSON(input) {
		return nil, ErrAlreadyUpstream
	}

	reg := &types.Registry{}
	if err := json.Unmarshal(input, reg); err != nil {
		return nil, fmt.Errorf("failed to parse legacy registry data: %w", err)
	}

	upstream, err := converters.NewUpstreamRegistryFromToolhiveRegistry(reg)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to upstream format: %w", err)
	}

	out, err := json.MarshalIndent(upstream, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upstream registry: %w", err)
	}

	if err := types.ValidateUpstreamRegistryBytes(out); err != nil {
		return nil, fmt.Errorf("converted output does not match the upstream registry schema: %w", err)
	}
	return out, nil
}

// isUpstreamJSON reports whether the JSON document appears to use the upstream
// registry format. The discriminator is a top-level "data" object — only the
// upstream format wraps servers inside it. The "$schema" key alone is not
// sufficient because the legacy format also includes one.
func isUpstreamJSON(data []byte) bool {
	var probe struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return len(probe.Data) > 0 && probe.Data[0] == '{'
}

// looksLikeLegacyJSON returns true if the JSON document has top-level fields
// characteristic of the legacy ToolHive registry format ("servers",
// "remote_servers", or "groups" at the top level rather than nested under a
// "data" object). Used to distinguish a legacy file from malformed or unrelated
// JSON so callers can emit a migration hint instead of a misleading
// "no servers" error.
//
// NOTE: keep in sync with looksLikeLegacyRegistryFormat in pkg/config/registry.go
// (duplicated to avoid a circular import — pkg/registry imports pkg/config).
func looksLikeLegacyJSON(data []byte) bool {
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
