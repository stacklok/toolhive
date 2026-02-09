// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeSkillDir creates a named skill directory inside t.TempDir() and writes SKILL.md to it.
// Returns the path to the skill directory.
func makeSkillDir(t *testing.T, dirName, skillMD string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), dirName)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o600))
	return dir
}

func TestValidateSkillDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setup        func(t *testing.T) string // returns path to skill dir
		wantValid    bool
		wantErrors   []string
		wantWarnings []string
	}{
		{
			name: "valid minimal skill",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "my-skill", "---\nname: my-skill\ndescription: A test skill\n---\n# My Skill\n")
			},
			wantValid: true,
		},
		{
			name: "valid full skill",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "my-full-skill", `---
name: my-full-skill
description: A comprehensive skill
version: 1.0.0
allowed-tools: Read Glob Grep
license: Apache-2.0
compatibility: claude
---
# My Full Skill

This skill does things.
`)
			},
			wantValid: true,
		},
		{
			name: "missing SKILL.md",
			setup: func(t *testing.T) string {
				t.Helper()
				return t.TempDir()
			},
			wantValid:  false,
			wantErrors: []string{"SKILL.md not found"},
		},
		{
			name: "invalid name - uppercase",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "MySkill", "---\nname: MySkill\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"invalid skill name"},
		},
		{
			name: "invalid name - starts with hyphen",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "-my-skill", "---\nname: -my-skill\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"invalid skill name"},
		},
		{
			name: "invalid name - ends with hyphen",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "my-skill-", "---\nname: my-skill-\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"invalid skill name"},
		},
		{
			name: "invalid name - consecutive hyphens",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "my--skill", "---\nname: my--skill\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"consecutive hyphens"},
		},
		{
			name: "invalid name - too long",
			setup: func(t *testing.T) string {
				t.Helper()
				longName := "a" + strings.Repeat("b", 63) + "c" // 65 chars
				return makeSkillDir(t, longName, fmt.Sprintf("---\nname: %s\ndescription: test\n---\n", longName))
			},
			wantValid:  false,
			wantErrors: []string{"invalid skill name"},
		},
		{
			name: "invalid name - single char",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "a", "---\nname: a\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"invalid skill name"},
		},
		{
			name: "missing name",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "no-name", "---\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"name is required"},
		},
		{
			name: "missing description",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "my-skill", "---\nname: my-skill\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"description is required"},
		},
		{
			name: "multiple errors",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "empty", "---\nname: \"\"\ndescription: \"\"\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"name is required", "description is required"},
		},
		{
			name: "invalid frontmatter",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "bad", "no frontmatter here")
			},
			wantValid:  false,
			wantErrors: []string{"invalid SKILL.md"},
		},
		{
			name: "name does not match directory",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "different-dir", "---\nname: my-skill\ndescription: test\n---\n")
			},
			wantValid:  false,
			wantErrors: []string{"must match directory name"},
		},
		{
			name: "description exceeds max length",
			setup: func(t *testing.T) string {
				t.Helper()
				longDesc := strings.Repeat("x", MaxDescriptionLength+1)
				return makeSkillDir(t, "long-desc", fmt.Sprintf("---\nname: long-desc\ndescription: %s\n---\n", longDesc))
			},
			wantValid:  false,
			wantErrors: []string{"description exceeds maximum length"},
		},
		{
			name: "compatibility exceeds max length",
			setup: func(t *testing.T) string {
				t.Helper()
				longCompat := strings.Repeat("x", MaxCompatibilityLength+1)
				return makeSkillDir(t, "compat-skill",
					fmt.Sprintf("---\nname: compat-skill\ndescription: test\ncompatibility: %s\n---\n", longCompat))
			},
			wantValid:  false,
			wantErrors: []string{"compatibility field exceeds maximum length"},
		},
		{
			name: "warning - large SKILL.md",
			setup: func(t *testing.T) string {
				t.Helper()
				var sb strings.Builder
				sb.WriteString("---\nname: large-skill\ndescription: test\n---\n")
				for range 600 {
					sb.WriteString("This is a line of content.\n")
				}
				return makeSkillDir(t, "large-skill", sb.String())
			},
			wantValid:    true,
			wantWarnings: []string{"SKILL.md has"},
		},
		{
			name: "valid two-char name",
			setup: func(t *testing.T) string {
				t.Helper()
				return makeSkillDir(t, "ab", "---\nname: ab\ndescription: test\n---\n")
			},
			wantValid: true,
		},
		{
			name: "valid 64-char name",
			setup: func(t *testing.T) string {
				t.Helper()
				longName := "a" + strings.Repeat("b", 62) + "c"
				return makeSkillDir(t, longName, fmt.Sprintf("---\nname: %s\ndescription: test\n---\n", longName))
			},
			wantValid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := tt.setup(t)

			result, err := ValidateSkillDir(dir)
			require.NoError(t, err)
			require.NotNil(t, result)

			assert.Equal(t, tt.wantValid, result.Valid,
				"valid=%v, errors=%v, warnings=%v", result.Valid, result.Errors, result.Warnings)

			for _, wantErr := range tt.wantErrors {
				assert.True(t, containsSubstring(result.Errors, wantErr),
					"expected error containing %q in %v", wantErr, result.Errors)
			}

			for _, wantWarn := range tt.wantWarnings {
				assert.True(t, containsSubstring(result.Warnings, wantWarn),
					"expected warning containing %q in %v", wantWarn, result.Warnings)
			}
		})
	}
}

func TestValidateSkillDir_Symlink(t *testing.T) {
	t.Parallel()

	dir := makeSkillDir(t, "sym-skill", "---\nname: sym-skill\ndescription: test\n---\n")

	// Create a real file and a symlink to it
	realFile := filepath.Join(dir, "real.txt")
	require.NoError(t, os.WriteFile(realFile, []byte("hello"), 0o600))

	symlinkPath := filepath.Join(dir, "link.txt")
	require.NoError(t, os.Symlink(realFile, symlinkPath))

	result, err := ValidateSkillDir(dir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.True(t, containsSubstring(result.Errors, "symlink found"),
		"expected symlink error in %v", result.Errors)
}

func TestValidateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid lowercase", input: "my-skill", wantErr: false},
		{name: "valid with numbers", input: "skill-v2", wantErr: false},
		{name: "valid min length", input: "ab", wantErr: false},
		{name: "valid all numbers", input: "123", wantErr: false},
		{name: "empty - skipped", input: "", wantErr: false},
		{name: "single char", input: "a", wantErr: true},
		{name: "uppercase", input: "MySkill", wantErr: true},
		{name: "starts with hyphen", input: "-skill", wantErr: true},
		{name: "ends with hyphen", input: "skill-", wantErr: true},
		{name: "consecutive hyphens", input: "my--skill", wantErr: true},
		{name: "contains underscore", input: "my_skill", wantErr: true},
		{name: "contains space", input: "my skill", wantErr: true},
		{name: "too long", input: "a" + strings.Repeat("b", 63) + "c", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// containsSubstring checks if any string in the slice contains the given substring.
func containsSubstring(strs []string, substr string) bool {
	for _, s := range strs {
		if strings.Contains(s, substr) {
			return true
		}
	}
	return false
}
