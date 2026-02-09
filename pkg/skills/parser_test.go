// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skills

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestParseSkillMD(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		content    string
		wantResult *ParseResult
		wantErr    string
	}{
		{
			name: "minimal frontmatter",
			content: `---
name: my-skill
description: A test skill
---
# My Skill

Some body content.
`,
			wantResult: &ParseResult{
				Name:        "my-skill",
				Description: "A test skill",
				Body:        []byte("# My Skill\n\nSome body content."),
			},
		},
		{
			name: "full frontmatter",
			content: `---
name: my-skill
description: A comprehensive test skill
version: 1.2.3
allowed-tools: Read Glob Grep
license: Apache-2.0
compatibility: claude
metadata:
  author: test-author
  category: testing
---
# My Skill
`,
			wantResult: &ParseResult{
				Name:          "my-skill",
				Description:   "A comprehensive test skill",
				Version:       "1.2.3",
				AllowedTools:  []string{"Read", "Glob", "Grep"},
				License:       "Apache-2.0",
				Compatibility: "claude",
				Metadata: map[string]string{
					"author":   "test-author",
					"category": "testing",
				},
				Body: []byte("# My Skill"),
			},
		},
		{
			name: "allowed-tools space-delimited",
			content: `---
name: space-tools
description: test
allowed-tools: Read Glob Grep Bash
---
`,
			wantResult: &ParseResult{
				Name:         "space-tools",
				Description:  "test",
				AllowedTools: []string{"Read", "Glob", "Grep", "Bash"},
				Body:         []byte(""),
			},
		},
		{
			name: "allowed-tools comma-delimited",
			content: `---
name: comma-tools
description: test
allowed-tools: Read, Glob, Grep
---
`,
			wantResult: &ParseResult{
				Name:         "comma-tools",
				Description:  "test",
				AllowedTools: []string{"Read", "Glob", "Grep"},
				Body:         []byte(""),
			},
		},
		{
			name: "allowed-tools yaml array",
			content: `---
name: array-tools
description: test
allowed-tools:
  - Read
  - Glob
  - Grep
---
`,
			wantResult: &ParseResult{
				Name:         "array-tools",
				Description:  "test",
				AllowedTools: []string{"Read", "Glob", "Grep"},
				Body:         []byte(""),
			},
		},
		{
			name: "allowed-tools empty string",
			content: `---
name: no-tools
description: test
allowed-tools: ""
---
`,
			wantResult: &ParseResult{
				Name:        "no-tools",
				Description: "test",
				Body:        []byte(""),
			},
		},
		{
			name: "dependencies from metadata",
			content: `---
name: with-deps
description: test
metadata:
  skillet.requires: |
    ghcr.io/org/skill-a:v1.0.0
    ghcr.io/org/skill-b:latest
---
`,
			wantResult: &ParseResult{
				Name:        "with-deps",
				Description: "test",
				Metadata: map[string]string{
					"skillet.requires": "ghcr.io/org/skill-a:v1.0.0\nghcr.io/org/skill-b:latest\n",
				},
				Requires: []Dependency{
					{Reference: "ghcr.io/org/skill-a:v1.0.0"},
					{Reference: "ghcr.io/org/skill-b:latest"},
				},
				Body: []byte(""),
			},
		},
		{
			name: "dependencies with blank lines",
			content: `---
name: deps-blanks
description: test
metadata:
  skillet.requires: |
    ghcr.io/org/skill-a:v1.0.0

    ghcr.io/org/skill-b:latest
---
`,
			wantResult: &ParseResult{
				Name:        "deps-blanks",
				Description: "test",
				Metadata: map[string]string{
					"skillet.requires": "ghcr.io/org/skill-a:v1.0.0\n\nghcr.io/org/skill-b:latest\n",
				},
				Requires: []Dependency{
					{Reference: "ghcr.io/org/skill-a:v1.0.0"},
					{Reference: "ghcr.io/org/skill-b:latest"},
				},
				Body: []byte(""),
			},
		},
		{
			name:    "missing opening delimiter",
			content: "name: my-skill\n---\n",
			wantErr: "invalid frontmatter",
		},
		{
			name:    "missing closing delimiter",
			content: "---\nname: my-skill\n",
			wantErr: "invalid frontmatter",
		},
		{
			name:    "empty content",
			content: "",
			wantErr: "invalid frontmatter",
		},
		{
			name:    "invalid yaml",
			content: "---\n: : invalid\n---\n",
			wantErr: "invalid frontmatter",
		},
		{
			name: "no body",
			content: `---
name: no-body
description: test
---`,
			wantResult: &ParseResult{
				Name:        "no-body",
				Description: "test",
				Body:        []byte(""),
			},
		},
		{
			name: "no metadata means no requires",
			content: `---
name: simple
description: test
---
`,
			wantResult: &ParseResult{
				Name:        "simple",
				Description: "test",
				Body:        []byte(""),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ParseSkillMD([]byte(tt.content))

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantResult.Name, result.Name)
			assert.Equal(t, tt.wantResult.Description, result.Description)
			assert.Equal(t, tt.wantResult.Version, result.Version)
			assert.Equal(t, tt.wantResult.AllowedTools, result.AllowedTools)
			assert.Equal(t, tt.wantResult.License, result.License)
			assert.Equal(t, tt.wantResult.Compatibility, result.Compatibility)
			assert.Equal(t, tt.wantResult.Body, result.Body)

			if tt.wantResult.Metadata != nil {
				assert.Equal(t, tt.wantResult.Metadata, result.Metadata)
			}
			if tt.wantResult.Requires != nil {
				assert.Equal(t, tt.wantResult.Requires, result.Requires)
			} else {
				assert.Nil(t, result.Requires)
			}
		})
	}
}

func TestParseSkillMD_FrontmatterSizeLimit(t *testing.T) {
	t.Parallel()

	t.Run("exceeds maximum size", func(t *testing.T) {
		t.Parallel()

		// Create frontmatter larger than MaxFrontmatterSize (64KB)
		largeValue := make([]byte, MaxFrontmatterSize+1)
		for i := range largeValue {
			largeValue[i] = 'a'
		}
		content := fmt.Sprintf("---\nname: %s\n---\n", string(largeValue))

		_, err := ParseSkillMD([]byte(content))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidFrontmatter)
		assert.Contains(t, err.Error(), "exceeds maximum size")
	})

	t.Run("at maximum size boundary", func(t *testing.T) {
		t.Parallel()

		// Create frontmatter exactly at MaxFrontmatterSize
		prefix := "name: boundary-test\ndescription: test\nmetadata:\n  padding: "
		padSize := MaxFrontmatterSize - len(prefix) - 1 // -1 for trailing newline
		padding := make([]byte, padSize)
		for i := range padding {
			padding[i] = 'x'
		}
		content := fmt.Sprintf("---\n%s%s\n---\nbody\n", prefix, string(padding))

		result, err := ParseSkillMD([]byte(content))
		require.NoError(t, err)
		assert.Equal(t, "boundary-test", result.Name)
	})
}

func TestParseSkillMD_DependencyLimit(t *testing.T) {
	t.Parallel()

	t.Run("exceeds maximum dependencies", func(t *testing.T) {
		t.Parallel()

		// Create more than MaxDependencies references
		var refs []string
		for i := range MaxDependencies + 1 {
			refs = append(refs, fmt.Sprintf("ghcr.io/org/skill-%d:v1.0.0", i))
		}
		requiresValue := strings.Join(refs, "\n")

		content := fmt.Sprintf("---\nname: too-many-deps\ndescription: test\nmetadata:\n  skillet.requires: |\n    %s\n---\n",
			strings.ReplaceAll(requiresValue, "\n", "\n    "))

		_, err := ParseSkillMD([]byte(content))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too many dependencies: more than")
	})

	t.Run("at maximum dependencies", func(t *testing.T) {
		t.Parallel()

		var refs []string
		for i := range MaxDependencies {
			refs = append(refs, fmt.Sprintf("ghcr.io/org/skill-%d:v1.0.0", i))
		}
		requiresValue := strings.Join(refs, "\n")

		content := fmt.Sprintf("---\nname: max-deps\ndescription: test\nmetadata:\n  skillet.requires: |\n    %s\n---\n",
			strings.ReplaceAll(requiresValue, "\n", "\n    "))

		result, err := ParseSkillMD([]byte(content))
		require.NoError(t, err)
		assert.Len(t, result.Requires, MaxDependencies)
	})
}

func TestStringOrSlice_UnmarshalYAML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		yaml    string
		want    []string
		wantErr bool
	}{
		{
			name: "space-delimited string",
			yaml: "tools: Read Glob Grep",
			want: []string{"Read", "Glob", "Grep"},
		},
		{
			name: "comma-delimited string",
			yaml: "tools: Read, Glob, Grep",
			want: []string{"Read", "Glob", "Grep"},
		},
		{
			name: "yaml array",
			yaml: "tools:\n  - Read\n  - Glob",
			want: []string{"Read", "Glob"},
		},
		{
			name: "single tool",
			yaml: "tools: Read",
			want: []string{"Read"},
		},
		{
			name: "empty string",
			yaml: `tools: ""`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var target struct {
				Tools StringOrSlice `yaml:"tools"`
			}
			err := yaml.Unmarshal([]byte(tt.yaml), &target)

			if tt.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, StringOrSlice(tt.want), target.Tools)
		})
	}
}
