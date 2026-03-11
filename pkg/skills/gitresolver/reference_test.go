// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gitresolver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsGitReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "valid git scheme", input: "git://github.com/org/repo", expected: true},
		{name: "with ref and path", input: "git://github.com/org/repo@v1#skills/foo", expected: true},
		{name: "plain name", input: "my-skill", expected: false},
		{name: "OCI reference", input: "ghcr.io/org/skill:v1", expected: false},
		{name: "https URL", input: "https://github.com/org/repo", expected: false},
		{name: "empty string", input: "", expected: false},
		{name: "git prefix but not scheme", input: "github.com/org/repo", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, IsGitReference(tt.input))
		})
	}
}

func TestParseGitReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       string
		expected    *GitReference
		expectError string
	}{
		{
			name:  "simple repo",
			input: "git://github.com/org/repo",
			expected: &GitReference{
				URL: "https://github.com/org/repo",
			},
		},
		{
			name:  "with tag ref",
			input: "git://github.com/org/repo@v1.0.0",
			expected: &GitReference{
				URL: "https://github.com/org/repo",
				Ref: "v1.0.0",
			},
		},
		{
			name:  "with path",
			input: "git://github.com/org/repo#skills/my-skill",
			expected: &GitReference{
				URL:  "https://github.com/org/repo",
				Path: "skills/my-skill",
			},
		},
		{
			name:  "with ref and path",
			input: "git://github.com/org/repo@main#skills/my-skill",
			expected: &GitReference{
				URL:  "https://github.com/org/repo",
				Ref:  "main",
				Path: "skills/my-skill",
			},
		},
		{
			name:  "gitlab host",
			input: "git://gitlab.com/org/repo",
			expected: &GitReference{
				URL: "https://gitlab.com/org/repo",
			},
		},
		{
			name:  "deep repo path",
			input: "git://github.com/org/suborg/repo",
			expected: &GitReference{
				URL: "https://github.com/org/suborg/repo",
			},
		},
		{
			name:        "not a git reference",
			input:       "my-skill",
			expectError: "not a git reference",
		},
		{
			name:        "empty after scheme",
			input:       "git://",
			expectError: "empty host/path",
		},
		{
			name:        "host only no repo",
			input:       "git://github.com",
			expectError: "no repository path after host",
		},
		{
			name:        "host with single path component",
			input:       "git://github.com/org",
			expectError: "repository path must be at least owner/repo",
		},
		{
			name:        "localhost rejected",
			input:       "git://localhost/org/repo",
			expectError: "SSRF prevention",
		},
		{
			name:        "127.0.0.1 rejected",
			input:       "git://127.0.0.1/org/repo",
			expectError: "SSRF prevention",
		},
		{
			name:        "private IP rejected",
			input:       "git://10.0.0.1/org/repo",
			expectError: "SSRF prevention",
		},
		{
			name:        "192.168 rejected",
			input:       "git://192.168.1.1/org/repo",
			expectError: "SSRF prevention",
		},
		{
			name:        "path traversal in skill path",
			input:       "git://github.com/org/repo#../../../etc/passwd",
			expectError: "'..' traversal",
		},
		{
			name:        "absolute skill path rejected",
			input:       "git://github.com/org/repo#/etc/passwd",
			expectError: "must be relative",
		},
		{
			name:        "backslash in skill path rejected",
			input:       "git://github.com/org/repo#skills\\my-skill",
			expectError: "must not contain backslashes",
		},
		{
			name:        "ref with shell metacharacters",
			input:       "git://github.com/org/repo@v1;rm -rf /",
			expectError: "invalid character",
		},
		{
			name:        "ref with double dots",
			input:       "git://github.com/org/repo@main..HEAD",
			expectError: "must not contain '..'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result, err := ParseGitReference(tt.input)

			if tt.expectError != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectError)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.expected.URL, result.URL)
			assert.Equal(t, tt.expected.Path, result.Path)
			assert.Equal(t, tt.expected.Ref, result.Ref)
		})
	}
}

func TestGitReference_SkillName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		ref      GitReference
		expected string
	}{
		{
			name:     "name from path",
			ref:      GitReference{URL: "https://github.com/org/repo", Path: "skills/my-skill"},
			expected: "my-skill",
		},
		{
			name:     "name from repo URL",
			ref:      GitReference{URL: "https://github.com/org/my-skill"},
			expected: "my-skill",
		},
		{
			name:     "name from repo URL with .git suffix",
			ref:      GitReference{URL: "https://github.com/org/my-skill.git"},
			expected: "my-skill",
		},
		{
			name:     "single path component",
			ref:      GitReference{URL: "https://github.com/org/repo", Path: "my-skill"},
			expected: "my-skill",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, tt.ref.SkillName())
		})
	}
}
