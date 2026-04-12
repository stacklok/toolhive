// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"

	v0 "github.com/modelcontextprotocol/registry/pkg/api/v0"

	"github.com/stacklok/toolhive-core/registry/converters"
	types "github.com/stacklok/toolhive-core/registry/types"
)

// ConvertServerJSONToMetadata converts an MCP Registry API ServerJSON to ToolHive
// ServerMetadata. It delegates to the official converters from toolhive-core.
//
// Only OCI packages and remote servers are handled; npm/pypi packages are
// skipped by design. Servers that have neither packages nor remotes are
// treated as incomplete and return an error.
//
// This function is intended for use at the execution boundary (retriever,
// workload service) where callers need ImageMetadata or RemoteServerMetadata
// rather than the raw ServerJSON stored in the Store.
func ConvertServerJSONToMetadata(serverJSON *v0.ServerJSON) (types.ServerMetadata, error) {
	if serverJSON == nil {
		return nil, fmt.Errorf("serverJSON is nil")
	}

	if len(serverJSON.Remotes) > 0 {
		return converters.ServerJSONToRemoteServerMetadata(serverJSON)
	}
	if len(serverJSON.Packages) == 0 {
		return nil, fmt.Errorf("server %s has no packages or remotes, skipping", serverJSON.Name)
	}
	// ServerJSONToImageMetadata only handles OCI packages; returns an error for npm/pypi.
	return converters.ServerJSONToImageMetadata(serverJSON)
}

// ConvertServersToServerMetadata converts a slice of ServerJSON to a slice of
// ServerMetadata. Servers that cannot be converted (e.g. incomplete entries or
// unsupported package types) are silently skipped so a single bad entry does
// not prevent the rest from being used.
func ConvertServersToServerMetadata(servers []*v0.ServerJSON) ([]types.ServerMetadata, error) {
	result := make([]types.ServerMetadata, 0, len(servers))
	for _, srv := range servers {
		md, err := ConvertServerJSONToMetadata(srv)
		if err != nil {
			// Skip unconvertible servers; the original ConvertServersToMetadata
			// in provider_api.go did the same.
			continue
		}
		result = append(result, md)
	}
	return result, nil
}
