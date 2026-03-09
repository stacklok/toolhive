// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEnv builds an EnvFunc that returns values from the given map.
func fakeEnv(vars map[string]string) EnvFunc {
	return func(key string) string {
		return vars[key]
	}
}

func TestResolveAuthWith(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cloneURL    string
		envVars     map[string]string
		expectNil   bool
		expectToken string
	}{
		{
			name:      "no env vars set",
			cloneURL:  "https://github.com/org/repo",
			envVars:   map[string]string{},
			expectNil: true,
		},
		{
			name:        "GITHUB_TOKEN sent to github.com",
			cloneURL:    "https://github.com/org/repo",
			envVars:     map[string]string{"GITHUB_TOKEN": "ghp_test123"},
			expectToken: "ghp_test123",
		},
		{
			name:      "GITHUB_TOKEN NOT sent to gitlab.com",
			cloneURL:  "https://gitlab.com/org/repo",
			envVars:   map[string]string{"GITHUB_TOKEN": "ghp_test123"},
			expectNil: true,
		},
		{
			name:      "GITHUB_TOKEN NOT sent to evil host",
			cloneURL:  "https://evil.com/org/repo",
			envVars:   map[string]string{"GITHUB_TOKEN": "ghp_secret"},
			expectNil: true,
		},
		{
			name:        "GITLAB_TOKEN sent to gitlab.com",
			cloneURL:    "https://gitlab.com/org/repo",
			envVars:     map[string]string{"GITLAB_TOKEN": "glpat-test123"},
			expectToken: "glpat-test123",
		},
		{
			name:      "GITLAB_TOKEN NOT sent to github.com",
			cloneURL:  "https://github.com/org/repo",
			envVars:   map[string]string{"GITLAB_TOKEN": "glpat-test123"},
			expectNil: true,
		},
		{
			name:        "GIT_TOKEN sent to any host",
			cloneURL:    "https://custom-git.example.com/org/repo",
			envVars:     map[string]string{"GIT_TOKEN": "token123"},
			expectToken: "token123",
		},
		{
			name:     "GITHUB_TOKEN takes precedence over GIT_TOKEN on github.com",
			cloneURL: "https://github.com/org/repo",
			envVars: map[string]string{
				"GITHUB_TOKEN": "ghp_first",
				"GIT_TOKEN":    "fallback",
			},
			expectToken: "ghp_first",
		},
		{
			name:     "GIT_TOKEN used on github.com when GITHUB_TOKEN absent",
			cloneURL: "https://github.com/org/repo",
			envVars: map[string]string{
				"GIT_TOKEN": "fallback",
			},
			expectToken: "fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			auth := ResolveAuthWith(fakeEnv(tt.envVars), tt.cloneURL)

			if tt.expectNil {
				assert.Nil(t, auth)
				return
			}

			require.NotNil(t, auth)
			basicAuth, ok := auth.(*githttp.BasicAuth)
			require.True(t, ok, "expected *githttp.BasicAuth")
			assert.Equal(t, "x-access-token", basicAuth.Username)
			assert.Equal(t, tt.expectToken, basicAuth.Password)
		})
	}
}
