// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"

	catalog "github.com/stacklok/toolhive-catalog/pkg/catalog/toolhive"
)

// EmbeddedRegistryName is the canonical name for the registry backed by the
// asset baked into the binary. It is what `thv registry list` displays for
// the default-shipped entry.
const EmbeddedRegistryName = "embedded"

// NewEmbeddedRegistry constructs a Registry from the registry data baked
// into the binary at build time. Used as the default registry in fresh
// installs.
//
// The optional name override lets callers override the default
// EmbeddedRegistryName — useful only for tests; production uses the
// constant.
func NewEmbeddedRegistry(name string) (Registry, error) {
	if name == "" {
		name = EmbeddedRegistryName
	}
	entries, err := parseEntries(catalog.Upstream())
	if err != nil {
		return nil, fmt.Errorf("embedded registry %q: %w", name, err)
	}
	return NewStore(name, entries)
}
