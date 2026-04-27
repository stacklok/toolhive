// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tui

import (
	"strings"
	"testing"

	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stretchr/testify/assert"
)

func TestSanitizeRegistryName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "dots and slashes replaced",
			input:    "io.github.stacklok/fetch",
			expected: "io-github-stacklok-fetch",
		},
		{
			name:     "multiple consecutive dots",
			input:    "a..b",
			expected: "a--b",
		},
		{
			name:     "no special characters",
			input:    "simple-name",
			expected: "simple-name",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "mixed dots slashes and dashes",
			input:    "io.github/org/tool.v2",
			expected: "io-github-org-tool-v2",
		},
		{
			name:     "only dots",
			input:    "...",
			expected: "---",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expected, sanitizeRegistryName(tc.input))
		})
	}
}

func TestBuildRunCmd(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		item     regtypes.ServerMetadata
		contains []string
		excludes []string
	}{
		{
			name: "minimal image metadata",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server"},
			},
			contains: []string{"thv run 'my-server'"},
			excludes: []string{"--transport", "--permission-profile", "--secret", "--env"},
		},
		{
			name: "non-default transport included",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server", Transport: "stdio"},
			},
			contains: []string{"--transport 'stdio'"},
		},
		{
			name: "default transport omitted",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server", Transport: "streamable-http"},
			},
			excludes: []string{"--transport"},
		},
		{
			name: "permission profile included when non-none",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server"},
				Permissions:        &permissions.Profile{Name: "network"},
			},
			contains: []string{"--permission-profile 'network'"},
		},
		{
			name: "permission profile 'none' omitted",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server"},
				Permissions:        &permissions.Profile{Name: "none"},
			},
			excludes: []string{"--permission-profile"},
		},
		{
			name: "required env var becomes --secret flag",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server"},
				EnvVars:            []*regtypes.EnvVar{{Name: "API_KEY", Required: true}},
			},
			contains: []string{"--secret 'API_KEY'"},
		},
		{
			name: "optional env var becomes comment",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server"},
				EnvVars:            []*regtypes.EnvVar{{Name: "LOG_LEVEL", Required: false}},
			},
			contains: []string{"# optional: --env 'LOG_LEVEL'=<value>"},
			excludes: []string{"--secret 'LOG_LEVEL'"},
		},
		{
			name: "transport and permission profile combined",
			item: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{Name: "my-server", Transport: "sse"},
				Permissions:        &permissions.Profile{Name: "network"},
			},
			contains: []string{"--transport 'sse'", "--permission-profile 'network'"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result := buildRunCmd(tc.item)
			for _, want := range tc.contains {
				assert.True(t, strings.Contains(result, want),
					"expected %q in output: %q", want, result)
			}
			for _, unwanted := range tc.excludes {
				assert.False(t, strings.Contains(result, unwanted),
					"unexpected %q in output: %q", unwanted, result)
			}
		})
	}
}

