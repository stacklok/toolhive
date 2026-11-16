// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"encoding/json"
	"log/slog"
	"sync"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
)

// embeddedCatalogNames returns the set of server names (container and remote)
// in the registry embedded in the binary, read once. Every local proxy is its
// own thv process, so it decodes only the names: parseRegistryData would build
// full metadata for every server, about 20 MB of allocations per process.
var embeddedCatalogNames = sync.OnceValue(func() map[string]struct{} {
	var upstream struct {
		Data struct {
			Servers []struct {
				Name string `json:"name"`
			} `json:"servers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(catalog.Upstream(), &upstream); err != nil {
		slog.Debug("failed to parse embedded registry", "error", err)
		return nil
	}
	names := make(map[string]struct{}, len(upstream.Data.Servers))
	for _, server := range upstream.Data.Servers {
		if server.Name != "" {
			names[server.Name] = struct{}{}
		}
	}
	return names
})

// IsEmbeddedCatalogServer reports whether name is an entry of the registry
// embedded in this binary. A server found through a custom local registry file
// is not an embedded entry unless the file reuses the same name.
func IsEmbeddedCatalogServer(name string) bool {
	if name == "" {
		return false
	}
	_, ok := embeddedCatalogNames()[name]
	return ok
}
