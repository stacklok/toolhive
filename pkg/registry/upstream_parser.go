// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"errors"
	"fmt"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	types "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry/legacyhint"
)

// errLegacyFormat is returned when the input looks like the legacy ToolHive
// registry format. Without this check, Go's JSON decoder silently produces an
// empty UpstreamRegistry (the legacy top-level "servers" field does not match
// upstream's "data.servers" path), leaving the caller with an empty registry
// and no actionable error. The error wording carries the migration step so
// consumers can surface it without a typed match.
var errLegacyFormat = errors.New(legacyhint.MigrationMessage)

// parseRegistryData parses raw JSON in the upstream MCP registry format and
// converts it into the internal types.Registry plus any embedded skills.
//
// Returns errLegacyFormat if the input looks like the legacy ToolHive registry
// format.
func parseRegistryData(data []byte) (*types.Registry, []types.Skill, error) {
	if !legacyhint.IsUpstream(data) && legacyhint.Looks(data) {
		return nil, nil, errLegacyFormat
	}

	var upstream types.UpstreamRegistry
	if err := json.Unmarshal(data, &upstream); err != nil {
		return nil, nil, fmt.Errorf("failed to parse registry data: %w", err)
	}

	// ConvertServersToMetadata expects []*v0.ServerJSON, but UpstreamData.Servers
	// is []v0.ServerJSON, so build a pointer slice.
	serverPtrs := make([]*v0.ServerJSON, len(upstream.Data.Servers))
	for i := range upstream.Data.Servers {
		serverPtrs[i] = &upstream.Data.Servers[i]
	}

	serverMetadata, err := ConvertServersToMetadata(serverPtrs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert servers to metadata: %w", err)
	}

	registry := &types.Registry{
		Version:       upstream.Version,
		LastUpdated:   upstream.Meta.LastUpdated,
		Servers:       make(map[string]*types.ImageMetadata),
		RemoteServers: make(map[string]*types.RemoteServerMetadata),
		Groups:        []*types.Group{},
	}

	for _, server := range serverMetadata {
		if server.IsRemote() {
			if remoteServer, ok := server.(*types.RemoteServerMetadata); ok {
				registry.RemoteServers[remoteServer.Name] = remoteServer
			}
		} else {
			if imageServer, ok := server.(*types.ImageMetadata); ok {
				registry.Servers[imageServer.Name] = imageServer
			}
		}
	}

	return registry, upstream.Data.Skills, nil
}
