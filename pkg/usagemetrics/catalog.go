// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import "sync/atomic"

// catalogLookup reports whether a name is an entry of the built-in registry.
var catalogLookup atomic.Pointer[func(string) bool]

// SetCatalogLookup registers the function that tells whether a server name is
// an entry of the registry embedded in the binary. Only binaries that embed the
// registry (thv) register one; the lookup lives outside this package so that
// binaries without it, such as thv-proxyrunner, do not pull the catalog in.
func SetCatalogLookup(lookup func(name string) bool) {
	catalogLookup.Store(&lookup)
}

// IsBuiltinCatalogServer reports whether name is an entry of the built-in
// registry. Without a registered lookup it returns false, so a name is never
// reported unless it has been checked against the catalog.
func IsBuiltinCatalogServer(name string) bool {
	lookup := catalogLookup.Load()
	return lookup != nil && name != "" && (*lookup)(name)
}
