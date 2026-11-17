// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
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

// refFrontmatter is the SKILL.md frontmatter used by reference tests; the
// body starts on line 5.
const refFrontmatter = "---\nname: sk\ndescription: d\n---\n"

// refHit is the part of a reference finding the tests compare.
type refHit struct {
	Rule string
	File string
	Line int
}

// writeTree writes files (slash-separated path -> content) under dir.
func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o750))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	}
}

// refHits validates dir with opts and returns its reference findings.
func refHits(t *testing.T, dir string, opts ...ValidateOption) []refHit {
	t.Helper()
	result, err := ValidateSkillDir(dir, opts...)
	require.NoError(t, err)
	require.True(t, result.Valid, "errors: %v", result.Errors)
	var hits []refHit
	for _, f := range result.Findings {
		switch f.Rule {
		case RuleRefUnresolved, RuleRefEscapesRoot, RuleRefFindingsTruncated:
			assert.Equal(t, SeverityWarning, f.Severity)
			assert.NotEmpty(t, f.Message)
			hits = append(hits, refHit{f.Rule, f.File, f.Line})
		}
	}
	return hits
}

func TestValidateSkillDir_References(t *testing.T) {
	t.Parallel()

	// Files present in every case besides SKILL.md.
	base := map[string]string{
		"scripts/x.sh":    "#!/bin/sh\n",
		"ref.md":          "# Ref\n",
		"my file.md":      "# Spaced\n",
		"assets/logo.png": "png",
	}

	tests := []struct {
		name  string
		body  string            // SKILL.md body, starting on line 5
		files map[string]string // extra files
		want  []refHit
	}{
		// ref-unresolved
		{name: "existing link", body: "[r](ref.md)\n"},
		{name: "existing directory link", body: "[s](scripts/)\n"},
		{name: "existing image", body: "![l](assets/logo.png)\n"},
		{name: "percent-encoded link", body: "[f](my%20file.md)\n"},
		{name: "fragment and line suffix stripped", body: "[a](ref.md#top) [b](ref.md:12) [c](ref.md?x=1)\n"},
		{
			name: "missing link",
			body: "intro\n\n[m](missing.md)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 7}},
		},
		{
			name: "missing image",
			body: "![m](img/none.png)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 5}},
		},
		{
			name: "missing link with fragment",
			body: "[m](missing.md#sec)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 5}},
		},
		{
			name: "empty link text uses block line",
			body: "\n[](missing.md)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 6}},
		},
		{
			name: "reference definition reported once at its line",
			body: "[a][x] and [x] and [b][x]\n\n[x]: missing.md\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 7}},
		},
		{
			name: "unused reference definition",
			body: "text\n\n[unused]: gone.md\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 7}},
		},
		{
			name: "each inline occurrence reported",
			body: "[a](gone.md)\n\n[b](gone.md)\n",
			want: []refHit{{RuleRefUnresolved, "SKILL.md", 5}, {RuleRefUnresolved, "SKILL.md", 7}},
		},

		// ref-escapes-root
		{
			name: "link escapes",
			body: "[s](../shared/x.md)\n",
			want: []refHit{{RuleRefEscapesRoot, "SKILL.md", 5}},
		},
		{name: "link staying inside", body: "[r](scripts/../ref.md)\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := makeSkillDir(t, "sk", refFrontmatter+tt.body)
			writeTree(t, dir, base)
			writeTree(t, dir, tt.files)
			assert.Equal(t, tt.want, refHits(t, dir))
		})
	}
}

func TestValidateSkillDir_ReferenceSkipRules(t *testing.T) {
	t.Parallel()

	// Each target would otherwise be unresolved or escaping.
	targets := []string{
		"https://example.com/../x.md",
		"mailto:a@b.c",
		"#anchor",
		"<placeholder>/../x.md",
		"${VAR}/../x.md",
		"$VAR/../x.md",
		"{{ dir }}/../x.md",
		"../*.md",
		"../[ab].md",
		"/abs/../x.md",
		"~/x/../../y.md",
		"C:/x/../../y.md",
	}

	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			body := fmt.Sprintf("[l](%s)\n\n[d]: %s\n", target, target)
			dir := makeSkillDir(t, "sk", refFrontmatter+body)
			assert.Empty(t, refHits(t, dir))
		})
	}
}

func TestValidateSkillDir_LinkedMarkdown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		body  string
		files map[string]string
		want  []refHit
	}{
		{
			name: "linked file is scanned relative to itself",
			body: "[a](docs/a.md)\n",
			files: map[string]string{
				"docs/a.md": "# A\n\n[b](b.md) [gone](gone.md)\n",
				"docs/b.md": "# B\n",
			},
			want: []refHit{{RuleRefUnresolved, "docs/a.md", 3}},
		},
		{
			name: "linked file lines count its frontmatter",
			body: "[a](a.md)\n",
			files: map[string]string{
				"a.md": "---\ntitle: a\n---\n\n[gone](gone.md)\n",
			},
			want: []refHit{{RuleRefUnresolved, "a.md", 5}},
		},
		{
			name: "cycle is scanned once",
			body: "[a](a.md)\n",
			files: map[string]string{
				"a.md": "[b](b.md) [gone](gone.md)\n",
				"b.md": "[a](a.md) [skill](SKILL.md)\n",
			},
			want: []refHit{{RuleRefUnresolved, "a.md", 1}},
		},
		{
			name: "non-markdown targets are not scanned",
			body: "[c](conf.txt)\n",
			files: map[string]string{
				"conf.txt": "[gone](gone.md)\n",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := makeSkillDir(t, "sk", refFrontmatter+tt.body)
			writeTree(t, dir, tt.files)
			assert.Equal(t, tt.want, refHits(t, dir))
		})
	}
}

func TestValidateSkillDir_ReferenceLimits(t *testing.T) {
	t.Parallel()

	t.Run("finding cap", func(t *testing.T) {
		t.Parallel()
		body := strings.Repeat("[x](gone.md)\n\n", maxRefFindings+5)
		hits := refHits(t, makeSkillDir(t, "sk", refFrontmatter+body))
		require.Len(t, hits, maxRefFindings+1)
		assert.Equal(t, refHit{RuleRefFindingsTruncated, "SKILL.md", 0}, hits[maxRefFindings])
	})

	t.Run("file cap", func(t *testing.T) {
		t.Parallel()
		files := map[string]string{}
		for i := range maxScannedFiles + 5 {
			files[fmt.Sprintf("f%d.md", i)] = fmt.Sprintf("[next](f%d.md)\n", i+1)
		}
		files[fmt.Sprintf("f%d.md", maxScannedFiles+5)] = ""
		dir := makeSkillDir(t, "sk", refFrontmatter+"[first](f0.md)\n")
		writeTree(t, dir, files)
		assert.Equal(t, []refHit{{RuleRefFindingsTruncated, "SKILL.md", 0}}, refHits(t, dir))
	})

	t.Run("oversized file is not scanned", func(t *testing.T) {
		t.Parallel()
		dir := makeSkillDir(t, "sk", refFrontmatter+"[big](big.md)\n")
		writeTree(t, dir, map[string]string{
			"big.md": "[gone](gone.md)\n" + strings.Repeat("x", maxScannedFileSize),
		})
		assert.Empty(t, refHits(t, dir))
	})
}

func TestValidateSkillDir_PluginRoot(t *testing.T) {
	t.Parallel()

	pluginDir := filepath.Join(t.TempDir(), "plugin")
	skillDir := filepath.Join(pluginDir, "skills", "sk")
	writeTree(t, pluginDir, map[string]string{
		"skills/sk/SKILL.md": refFrontmatter +
			"[shared](../../shared/x.md)\n\n[out](../../../outside.md)\n\n[gone](gone.md)\n",
		"shared/x.md": "[not scanned](missing.md)\n",
	})

	assert.Equal(t, []refHit{
		{RuleRefEscapesRoot, "skills/sk/SKILL.md", 7},
		{RuleRefUnresolved, "skills/sk/SKILL.md", 9},
	}, refHits(t, skillDir, WithPluginRoot(pluginDir)))

	// Standalone, the same skill cannot reach the plugin's shared file.
	assert.Equal(t, []refHit{
		{RuleRefEscapesRoot, "SKILL.md", 5},
		{RuleRefEscapesRoot, "SKILL.md", 7},
		{RuleRefUnresolved, "SKILL.md", 9},
	}, refHits(t, skillDir))
}

func TestValidateSkillDir_PluginRootInvalid(t *testing.T) {
	t.Parallel()

	skillDir := makeSkillDir(t, "sk", refFrontmatter)
	for name, root := range map[string]string{
		"relative":          "plugin",
		"does not contain":  t.TempDir(),
		"inside skill only": filepath.Join(skillDir, "sub"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSkillDir(skillDir, WithPluginRoot(root))
			require.Error(t, err)
			assert.Contains(t, err.Error(), "plugin root")
		})
	}
}
