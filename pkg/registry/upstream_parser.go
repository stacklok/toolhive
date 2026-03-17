// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"fmt"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	types "github.com/stacklok/toolhive-core/registry/types"
)

// parseUpstreamRegistry parses raw JSON in the upstream registry format and
// converts it into a legacy types.Registry plus any embedded skills.
func parseUpstreamRegistry(data []byte) (*types.Registry, []types.Skill, error) {
	var upstream types.UpstreamRegistry
	if err := json.Unmarshal(data, &upstream); err != nil {
		return nil, nil, fmt.Errorf("failed to parse upstream registry data: %w", err)
	}

	// ConvertServersToMetadata expects []*v0.ServerJSON, but UpstreamData.Servers
	// is []v0.ServerJSON, so build a pointer slice.
	serverPtrs := make([]*v0.ServerJSON, len(upstream.Data.Servers))
	for i := range upstream.Data.Servers {
		serverPtrs[i] = &upstream.Data.Servers[i]
	}

	serverMetadata, err := ConvertServersToMetadata(serverPtrs)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to convert upstream servers to metadata: %w", err)
	}

	// Build the legacy Registry, separating container and remote servers.
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

// upstreamFormatProbe is a minimal struct used to detect whether JSON data is
// in the upstream registry format without fully unmarshalling it.
type upstreamFormatProbe struct {
	Schema string          `json:"$schema"`
	Data   json.RawMessage `json:"data"`
}

// isUpstreamFormat returns true when the raw JSON appears to be in the upstream
// registry format. The key discriminator is the "data" wrapper object — only
// the upstream format wraps servers inside a "data" object. The "$schema" key
// alone is not sufficient because the legacy format also includes one.
// NOTE: keep in sync with isUpstreamRegistryFormat in pkg/config/registry.go
// (duplicated to avoid a circular import).
func isUpstreamFormat(data []byte) bool {
	var probe upstreamFormatProbe
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	// The "data" wrapper object is unique to the upstream format.
	return len(probe.Data) > 0 && probe.Data[0] == '{'
}

// parseRegistryAutoDetect attempts to parse the given JSON data by first
// checking whether it uses the upstream format. If so it delegates to
// parseUpstreamRegistry; otherwise it falls back to the legacy parser
// (parseRegistryData). The returned isLegacy flag indicates which path was
// taken. Skills are only returned for the upstream format.
func parseRegistryAutoDetect(data []byte) (*types.Registry, []types.Skill, bool, error) {
	if isUpstreamFormat(data) {
		reg, skills, err := parseUpstreamRegistry(data)
		if err != nil {
			return nil, nil, false, fmt.Errorf("upstream format detected but parsing failed: %w", err)
		}
		return reg, skills, false, nil
	}

	// Legacy format — no skills.
	reg, err := parseRegistryData(data)
	if err != nil {
		return nil, nil, true, fmt.Errorf("failed to parse legacy registry data: %w", err)
	}
	return reg, nil, true, nil
}
