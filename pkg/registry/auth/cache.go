// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"

	"github.com/adrg/xdg"
)

const (
	// PersistentCacheSubdir is the subdirectory under toolhive's XDG cache for registry data.
	PersistentCacheSubdir = "cache"
)

// RegistryCacheFilePath returns the XDG cache file path for the given registry URL.
// This creates intermediate directories if needed (suitable for write operations).
func RegistryCacheFilePath(registryURL string) (string, error) {
	hash := sha256.Sum256([]byte(registryURL))
	return xdg.CacheFile(fmt.Sprintf("toolhive/%s/registry-%x.json", PersistentCacheSubdir, hash[:4]))
}

// registryCachePath returns the cache file path without creating directories.
// Suitable for read or delete operations where directory creation is undesirable.
func registryCachePath(registryURL string) string {
	hash := sha256.Sum256([]byte(registryURL))
	return filepath.Join(xdg.CacheHome, "toolhive", PersistentCacheSubdir,
		fmt.Sprintf("registry-%x.json", hash[:4]))
}
