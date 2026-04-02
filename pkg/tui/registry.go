// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
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

// sanitizeRegistryName replaces dots and slashes with dashes for use as a workload name.
func sanitizeRegistryName(name string) string {
	r := strings.NewReplacer(".", "-", "/", "-")
	return r.Replace(name)
}

// buildRunFormFields creates form fields from a registry item's metadata.
func buildRunFormFields(item regtypes.ServerMetadata) []formField {
	var fields []formField

	// First field: workload name (pre-filled, required).
	nameInput := textinput.New()
	nameInput.Placeholder = "workload name"
	nameInput.SetValue(sanitizeRegistryName(item.GetName()))
	nameInput.CharLimit = 64
	fields = append(fields, formField{
		input:    nameInput,
		name:     "name",
		required: true,
		desc:     "Name for the running workload",
	})

	// One field per env var declared by the server.
	for _, ev := range item.GetEnvVars() {
		if ev == nil {
			continue
		}
		ti := textinput.New()
		ti.Placeholder = ev.Name
		if ev.Default != "" {
			ti.SetValue(ev.Default)
		}
		if ev.Secret {
			ti.EchoMode = textinput.EchoPassword
		}
		fields = append(fields, formField{
			input:    ti,
			name:     ev.Name,
			required: ev.Required,
			desc:     ev.Description,
			secret:   ev.Secret,
		})
	}

	return fields
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
