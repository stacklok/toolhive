// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/registry"
)

// registryLoadedMsg is sent when the registry server list has been fetched.
type registryLoadedMsg struct {
	items []regtypes.ServerMetadata
	err   error
}

// fetchRegistryItems returns a tea.Cmd that loads all servers from the registry.
func fetchRegistryItems(_ context.Context) tea.Cmd {
	return func() tea.Msg {
		provider, err := registry.GetDefaultProvider()
		if err != nil {
			return registryLoadedMsg{err: err}
		}
		items, err := provider.ListServers()
		return registryLoadedMsg{items: items, err: err}
	}
}

// filterRegistryItems returns items whose name or description contains query.
func filterRegistryItems(items []regtypes.ServerMetadata, query string) []regtypes.ServerMetadata {
	if query == "" {
		return items
	}
	q := strings.ToLower(query)
	var out []regtypes.ServerMetadata
	for _, item := range items {
		if strings.Contains(strings.ToLower(item.GetName()), q) ||
			strings.Contains(strings.ToLower(item.GetDescription()), q) {
			out = append(out, item)
		}
	}
	return out
}
