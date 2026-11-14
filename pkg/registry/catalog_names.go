// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"log/slog"
	"sync"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
)

// embeddedCatalogNames parses the registry embedded in the binary once and
// returns the set of its server names (container and remote).
var embeddedCatalogNames = sync.OnceValue(func() map[string]struct{} {
	reg, _, _, err := parseRegistryData(catalog.Upstream())
	if err != nil {
		slog.Debug("failed to parse embedded registry", "error", err)
		return nil
	}
	names := make(map[string]struct{}, len(reg.Servers)+len(reg.RemoteServers))
	for name := range reg.Servers {
		names[name] = struct{}{}
	}
	for name := range reg.RemoteServers {
		names[name] = struct{}{}
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
