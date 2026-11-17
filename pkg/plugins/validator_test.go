// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package plugins

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
)

// writePluginDir creates a plugin directory at dir/name with a manifest and
// optional bundled content. manifestBody is written to .claude-plugin/plugin.json.
// name is the directory name and is kept distinct from the manifest's "name"
// field so mismatch tests can construct dir-name ≠ manifest-name fixtures.
//
//nolint:unparam // name is always "my-plugin" in current tests but is required for mismatch fixtures.
func writePluginDir(t *testing.T, name, manifestBody string) string {
	t.Helper()
	dir := t.TempDir()
	pluginDir := filepath.Join(dir, name)
	require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, ".claude-plugin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pluginDir, ManifestPath), []byte(manifestBody), 0o600))
	return pluginDir
}

func TestValidatePluginDir_ValidWithBundledSkill(t *testing.T) {
	t.Parallel()
	manifest := `{
		"name": "my-plugin",
		"version": "1.0.0",
		"description": "test",
		"keywords": ["a"],
		"skills": ["./skills/foo"]
	}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	// Bundled skill: ./skills/foo with SKILL.md frontmatter name == "foo".
	skillDir := filepath.Join(pluginDir, "skills", "foo")
	require.NoError(t, os.MkdirAll(skillDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: foo\ndescription: a bundled skill\n---\n# Foo\n"), 0o600))

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.True(t, result.Valid, "errors: %v", result.Errors)
}

func TestValidatePluginDir_MissingManifest(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// empty dir, no .claude-plugin/plugin.json
	result, err := ValidatePluginDir(dir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "plugin.json")
}

func TestValidatePluginDir_NameMismatch(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"other","description":"x"}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "must match directory name")
}

func TestValidatePluginDir_BundledSkillValidationReuse(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"my-plugin","description":"x","skills":["./skills/bad"]}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	// Bundled skill missing the required name in frontmatter — the skills
	// validator must surface its message.
	skillDir := filepath.Join(pluginDir, "skills", "bad")
	require.NoError(t, os.MkdirAll(skillDir, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\ndescription: no name here\n---\n# Bad\n"), 0o600))

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "bundled skill")
	assert.Contains(t, joinErrors(result.Errors), "name is required")
}

func TestValidatePluginDir_SymlinkRejected(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"my-plugin","description":"x"}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	// Create a symlink inside the plugin dir — CheckFilesystem must reject it.
	target := filepath.Join(t.TempDir(), "target")
	require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(pluginDir, "link.md")))

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "symlink")
}

func TestValidatePluginDir_TraversalInManifest(t *testing.T) {
	t.Parallel()
	manifest := `{"name":"my-plugin","description":"x","skills":["../escape"]}`
	pluginDir := writePluginDir(t, "my-plugin", manifest)

	result, err := ValidatePluginDir(pluginDir)
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Contains(t, joinErrors(result.Errors), "must be relative and start with ./")
}

func TestValidatePluginDir_RelativePath(t *testing.T) {
	t.Parallel()
	_, err := ValidatePluginDir("relative/path")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

func TestValidatePluginDir_EmptyPath(t *testing.T) {
	t.Parallel()
	_, err := ValidatePluginDir("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute")
}

// joinErrors concatenates error strings for substring assertions.
func joinErrors(errs []string) string {
	out := ""
	for _, e := range errs {
		out += e + "\n"
	}
	return out
}

func TestValidatePluginDir_Findings(t *testing.T) {
	t.Parallel()

	writeSkill := func(t *testing.T, pluginDir, rel, skillMD string) {
		t.Helper()
		dir := filepath.Join(pluginDir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(dir, 0o750))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o600))
	}

	tests := []struct {
		name         string
		manifest     string // empty: no manifest written
		setup        func(t *testing.T, pluginDir string)
		wantValid    bool
		wantFindings []skills.Finding // Message is not compared
		// wantPrefix is the legacy prefix expected on every Errors/Warnings entry.
		wantPrefix string
	}{
		{
			name:      "missing manifest",
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleManifestInvalid, File: ManifestPath},
			},
		},
		{
			name:      "name mismatch",
			manifest:  `{"name":"other","description":"x"}`,
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleNameDirMismatch, File: ManifestPath},
			},
		},
		{
			name:      "invalid name",
			manifest:  `{"name":"Bad","description":"x"}`,
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleNameInvalid, File: ManifestPath},
				{Severity: skills.SeverityError, Rule: skills.RuleNameDirMismatch, File: ManifestPath},
			},
		},
		{
			name:      "missing name",
			manifest:  `{"description":"x"}`,
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleNameRequired, File: ManifestPath},
			},
		},
		{
			name:     "symlink",
			manifest: `{"name":"my-plugin","description":"x"}`,
			setup: func(t *testing.T, pluginDir string) {
				t.Helper()
				target := filepath.Join(t.TempDir(), "target")
				require.NoError(t, os.WriteFile(target, []byte("x"), 0o600))
				require.NoError(t, os.Symlink(target, filepath.Join(pluginDir, "link.md")))
			},
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleSymlinkNotAllowed, File: "link.md"},
			},
		},
		{
			name:     "bundled skill error is relative to the plugin",
			manifest: `{"name":"my-plugin","description":"x","skills":["./skills/bad"]}`,
			setup: func(t *testing.T, pluginDir string) {
				t.Helper()
				writeSkill(t, pluginDir, "skills/bad", "---\ndescription: no name here\n---\n# Bad\n")
			},
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleNameRequired, File: "skills/bad/SKILL.md"},
			},
			wantPrefix: `bundled skill "./skills/bad": `,
		},
		{
			name:     "bundled skill warning is reported",
			manifest: `{"name":"my-plugin","description":"x","skills":["./skills/foo"]}`,
			setup: func(t *testing.T, pluginDir string) {
				t.Helper()
				writeSkill(t, pluginDir, "skills/foo",
					"---\nname: foo\ndescription: x\nallowed-tools: Read, Grep\n---\n# Foo\n")
			},
			wantValid: true,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityWarning, Rule: skills.RuleAllowedToolsComma, File: "skills/foo/SKILL.md"},
			},
			wantPrefix: `bundled skill "./skills/foo": `,
		},
		{
			name:     "bundled skill references are contained by the plugin",
			manifest: `{"name":"my-plugin","description":"x","skills":["./skills/foo"]}`,
			setup: func(t *testing.T, pluginDir string) {
				t.Helper()
				writeSkill(t, pluginDir, "skills/foo", "---\nname: foo\ndescription: x\n---\n"+
					"[shared](../../shared/x.md)\n\n```bash\n./scripts/x.sh\ncat ../../../outside.md\n```\n")
				require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "shared"), 0o750))
				require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "shared", "x.md"), []byte("# X\n"), 0o600))
				require.NoError(t, os.MkdirAll(filepath.Join(pluginDir, "skills", "foo", "scripts"), 0o750))
				require.NoError(t, os.WriteFile(filepath.Join(pluginDir, "skills", "foo", "scripts", "x.sh"), nil, 0o600))
			},
			wantValid: true,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityWarning, Rule: skills.RuleRefEscapesRoot, File: "skills/foo/SKILL.md", Line: 9},
			},
			wantPrefix: `bundled skill "./skills/foo": `,
		},
		{
			name:      "bundled skill directory missing",
			manifest:  `{"name":"my-plugin","description":"x","skills":["./skills/gone"]}`,
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleSkillMDMissing, File: "skills/gone/SKILL.md"},
			},
			wantPrefix: `bundled skill "./skills/gone": `,
		},
		{
			name:     "bundled skill unreadable",
			manifest: `{"name":"my-plugin","description":"x","skills":["./skills/locked"]}`,
			setup: func(t *testing.T, pluginDir string) {
				t.Helper()
				if runtime.GOOS == "windows" || os.Geteuid() == 0 {
					t.Skip("file mode 0000 does not block reads on Windows or for root")
				}
				writeSkill(t, pluginDir, "skills/locked", "---\nname: locked\ndescription: x\n---\n")
				skillMD := filepath.Join(pluginDir, "skills", "locked", "SKILL.md")
				require.NoError(t, os.Chmod(skillMD, 0o000))
				t.Cleanup(func() { _ = os.Chmod(skillMD, 0o600) })
			},
			wantValid: false,
			wantFindings: []skills.Finding{
				{Severity: skills.SeverityError, Rule: skills.RuleBundledSkillUnreadable, File: "skills/locked"},
			},
			wantPrefix: `bundled skill "./skills/locked": `,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pluginDir := filepath.Join(t.TempDir(), "my-plugin")
			require.NoError(t, os.MkdirAll(pluginDir, 0o750))
			if tt.manifest != "" {
				pluginDir = writePluginDir(t, "my-plugin", tt.manifest)
			}
			if tt.setup != nil {
				tt.setup(t, pluginDir)
			}

			result, err := ValidatePluginDir(pluginDir)
			require.NoError(t, err)
			assert.Equal(t, tt.wantValid, result.Valid, "errors: %v", result.Errors)

			got := make([]skills.Finding, len(result.Findings))
			for i, f := range result.Findings {
				assert.NotEmpty(t, f.Message)
				assert.NotContains(t, f.Message, "bundled skill", "message must not carry the legacy prefix")
				f.Message = ""
				got[i] = f
			}
			assert.Equal(t, tt.wantFindings, got)

			var errs, warns []string
			for _, f := range result.Findings {
				if f.Severity == skills.SeverityError {
					errs = append(errs, tt.wantPrefix+f.Message)
				} else {
					warns = append(warns, tt.wantPrefix+f.Message)
				}
			}
			assert.Equal(t, errs, result.Errors)
			assert.Equal(t, warns, result.Warnings)
		})
	}
}
