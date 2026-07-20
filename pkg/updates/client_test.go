// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package updates

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewVersionClientForComponent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		component      string
		version        string
		uiReleaseBuild bool
		expected       string
	}{
		{
			name:           "CLI component",
			component:      "CLI",
			version:        "",
			uiReleaseBuild: false,
			expected:       "CLI",
		},
		{
			name:           "operator component",
			component:      "operator",
			version:        "",
			uiReleaseBuild: false,
			expected:       "operator",
		},
		{
			name:           "UI component with version and release build",
			component:      "UI",
			version:        "2.0.0",
			uiReleaseBuild: true,
			expected:       "UI",
		},
		{
			name:           "UI component with version and local build",
			component:      "UI",
			version:        "2.0.0",
			uiReleaseBuild: false,
			expected:       "UI",
		},
		{
			name:           "API component",
			component:      "API",
			version:        "",
			uiReleaseBuild: false,
			expected:       "API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client := NewVersionClientForComponent(tt.component, tt.version, tt.uiReleaseBuild)
			defaultClient, ok := client.(*defaultVersionClient)
			assert.True(t, ok, "Expected defaultVersionClient type")
			assert.Equal(t, tt.expected, defaultClient.component)
			assert.Equal(t, tt.version, defaultClient.customVersion)
			assert.Equal(t, tt.uiReleaseBuild, defaultClient.uiReleaseBuild)
		})
	}
}

func TestShouldSkipUpdateChecks_SkipEnvVar(t *testing.T) {
	// Not parallel: mutates process environment via t.Setenv, including the
	// CI variables that other tests in this binary may also rely on.
	tests := []struct {
		name    string
		value   string
		wantSet bool
	}{
		{name: "unset", value: "", wantSet: false},
		{name: "true", value: "true", wantSet: true},
		{name: "TRUE uppercase", value: "TRUE", wantSet: true},
		{name: "false does not skip", value: "false", wantSet: false},
		{name: "unrecognized value does not skip", value: "maybe", wantSet: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear CI vars so the opt-out path is exercised in isolation,
			// independent of whether the test runs under GitHub Actions.
			for _, v := range ciEnvVars {
				t.Setenv(v, "")
			}
			t.Setenv(EnvVarSkipUpdateCheck, tt.value)
			assert.Equal(t, tt.wantSet, ShouldSkipUpdateChecks())
		})
	}
}
