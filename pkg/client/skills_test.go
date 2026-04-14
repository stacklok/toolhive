// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
)

// testSkillClientIntegrations returns a minimal set of client configs for testing.
func testSkillClientIntegrations() []clientAppConfig {
	return []clientAppConfig{
		{
			ClientType:        ClaudeCode,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".claude", "skills"},
			SkillsProjectPath: []string{".claude", "skills"},
		},
		{
			ClientType:        Codex,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".agents", "skills"},
			SkillsProjectPath: []string{".agents", "skills"},
		},
		{
			ClientType:        OpenCode,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{"opencode", "skills"},
			SkillsProjectPath: []string{".opencode", "skills"},
			SkillsPlatformPrefix: map[Platform][]string{
				PlatformLinux:  {".config"},
				PlatformDarwin: {".config"},
			},
		},
		{
			ClientType:        Cursor,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".cursor", "skills"},
			SkillsProjectPath: []string{".cursor", "skills"},
		},
		{
			ClientType:        KimiCli,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".kimi", "skills"},
			SkillsProjectPath: []string{".kimi", "skills"},
		},
		{
			ClientType:        Factory,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".factory", "skills"},
			SkillsProjectPath: []string{".factory", "skills"},
		},
		{
			ClientType:        VSCode,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".copilot", "skills"},
			SkillsProjectPath: []string{".github", "skills"},
		},
		{
			ClientType:        VSCodeInsider,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".copilot", "skills"},
			SkillsProjectPath: []string{".github", "skills"},
		},
		{
			ClientType:        Goose,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".agents", "skills"},
			SkillsProjectPath: []string{".agents", "skills"},
		},
		{
			ClientType:        GeminiCli,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".agents", "skills"},
			SkillsProjectPath: []string{".agents", "skills"},
		},
		{
			ClientType:        AmpCli,
			SupportsSkills:    true,
			SkillsGlobalPath:  []string{".agents", "skills"},
			SkillsProjectPath: []string{".agents", "skills"},
		},
		{
			ClientType: Windsurf,
			// SupportsSkills defaults to false
		},
		{
			// A test-only client that supports skills but has no paths configured.
			ClientType:     ClientApp("no-paths-client"),
			SupportsSkills: true,
		},
	}
}

const testHomeDir = "/fake/home"

func newTestSkillManager() *ClientManager {
	return NewTestClientManager(testHomeDir, nil, testSkillClientIntegrations(), nil)
}

func TestSupportsSkills(t *testing.T) {
	t.Parallel()
	cm := newTestSkillManager()

	tests := []struct {
		name     string
		client   ClientApp
		expected bool
	}{
		{name: "ClaudeCode supports skills", client: ClaudeCode, expected: true},
		{name: "Codex supports skills", client: Codex, expected: true},
		{name: "OpenCode supports skills", client: OpenCode, expected: true},
		{name: "Cursor supports skills", client: Cursor, expected: true},
		{name: "KimiCli supports skills", client: KimiCli, expected: true},
		{name: "VSCode supports skills", client: VSCode, expected: true},
		{name: "VSCodeInsider supports skills", client: VSCodeInsider, expected: true},
		{name: "Factory supports skills", client: Factory, expected: true},
		{name: "Goose supports skills", client: Goose, expected: true},
		{name: "GeminiCli supports skills", client: GeminiCli, expected: true},
		{name: "AmpCli supports skills", client: AmpCli, expected: true},
		{name: "Windsurf does not support skills", client: Windsurf, expected: false},
		{name: "unknown client returns false", client: ClientApp("nonexistent"), expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, cm.SupportsSkills(tt.client))
		})
	}
}

func TestListSkillSupportingClients(t *testing.T) {
	t.Parallel()
	cm := newTestSkillManager()
	clients := cm.ListSkillSupportingClients()

	// Should include AmpCli, ClaudeCode, Codex, Cursor, Factory, GeminiCli, Goose, KimiCli, OpenCode, VSCode, VSCodeInsider, and our test-only no-paths-client
	require.Len(t, clients, 12, "unexpected number of skill-supporting clients: %v", clients)

	// Verify sorted order
	for i := 1; i < len(clients); i++ {
		assert.True(t, clients[i-1] < clients[i],
			"not sorted: %q comes after %q", clients[i], clients[i-1])
	}
}

func TestGetSkillPath(t *testing.T) {
	t.Parallel()
	cm := newTestSkillManager()

	tests := []struct {
		name           string
		client         ClientApp
		skillName      string
		scope          skills.Scope
		projectRoot    string
		wantPath       string // exact expected path
		wantErr        error  // sentinel error to check with errors.Is (nil = no error)
		wantErrContain string // substring to check in error message (for non-sentinel errors)
	}{
		{
			name:      "ScopeUser ClaudeCode",
			client:    ClaudeCode,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".claude", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser Codex",
			client:    Codex,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".agents", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser OpenCode",
			client:    OpenCode,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".config", "opencode", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject ClaudeCode with explicit root",
			client:      ClaudeCode,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".claude", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject Codex with explicit root",
			client:      Codex,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".agents", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject OpenCode with explicit root",
			client:      OpenCode,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".opencode", "skills", "my-skill"),
		},
		{
			name:      "ScopeProject requires projectRoot",
			client:    ClaudeCode,
			skillName: "my-skill",
			scope:     skills.ScopeProject,
			wantErr:   ErrProjectRootRequired,
		},
		{
			name:        "ScopeProject no project path configured",
			client:      ClientApp("no-paths-client"),
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantErr:     ErrNoSkillPath,
		},
		{
			name:      "ScopeUser no global path configured",
			client:    ClientApp("no-paths-client"),
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantErr:   ErrNoSkillPath,
		},
		{
			name:      "invalid client",
			client:    ClientApp("nonexistent"),
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantErr:   ErrUnsupportedClientType,
		},
		{
			name:      "ScopeUser Cursor",
			client:    Cursor,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".cursor", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject Cursor with explicit root",
			client:      Cursor,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".cursor", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser KimiCli",
			client:    KimiCli,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".kimi", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject KimiCli with explicit root",
			client:      KimiCli,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".kimi", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser Factory",
			client:    Factory,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".factory", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject Factory with explicit root",
			client:      Factory,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".factory", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser VSCode",
			client:    VSCode,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".copilot", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject VSCode with explicit root",
			client:      VSCode,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".github", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser VSCodeInsider",
			client:    VSCodeInsider,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".copilot", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject VSCodeInsider with explicit root",
			client:      VSCodeInsider,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".github", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser Goose",
			client:    Goose,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".agents", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject Goose with explicit root",
			client:      Goose,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".agents", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser GeminiCli",
			client:    GeminiCli,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".agents", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject GeminiCli with explicit root",
			client:      GeminiCli,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".agents", "skills", "my-skill"),
		},
		{
			name:      "ScopeUser AmpCli",
			client:    AmpCli,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantPath:  filepath.Join(testHomeDir, ".agents", "skills", "my-skill"),
		},
		{
			name:        "ScopeProject AmpCli with explicit root",
			client:      AmpCli,
			skillName:   "my-skill",
			scope:       skills.ScopeProject,
			projectRoot: "/tmp/myproject",
			wantPath:    filepath.Join("/tmp/myproject", ".agents", "skills", "my-skill"),
		},
		{
			name:      "client that does not support skills",
			client:    Windsurf,
			skillName: "my-skill",
			scope:     skills.ScopeUser,
			wantErr:   ErrSkillsNotSupported,
		},
		{
			name:      "unknown scope",
			client:    ClaudeCode,
			skillName: "my-skill",
			scope:     skills.Scope("global"),
			wantErr:   ErrUnknownScope,
		},
		// Skill name validation (delegated to skills.ValidateSkillName)
		{
			name:           "empty skill name",
			client:         ClaudeCode,
			skillName:      "",
			scope:          skills.ScopeUser,
			wantErrContain: "invalid skill name",
		},
		{
			name:           "path traversal with slashes",
			client:         ClaudeCode,
			skillName:      "../../etc/passwd",
			scope:          skills.ScopeUser,
			wantErrContain: "invalid skill name",
		},
		{
			name:           "path traversal with backslash",
			client:         ClaudeCode,
			skillName:      `foo\bar`,
			scope:          skills.ScopeUser,
			wantErrContain: "invalid skill name",
		},
		{
			name:           "uppercase rejected",
			client:         ClaudeCode,
			skillName:      "MySkill",
			scope:          skills.ScopeUser,
			wantErrContain: "invalid skill name",
		},
		{
			name:           "consecutive hyphens rejected",
			client:         ClaudeCode,
			skillName:      "my--skill",
			scope:          skills.ScopeUser,
			wantErrContain: "consecutive hyphens",
		},
		{
			name:           "null byte rejected",
			client:         ClaudeCode,
			skillName:      "skill\x00evil",
			scope:          skills.ScopeUser,
			wantErrContain: "invalid skill name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := cm.GetSkillPath(tt.client, tt.skillName, tt.scope, tt.projectRoot)
			if tt.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tt.wantErr),
					"expected error wrapping %v, got: %v", tt.wantErr, err)
				return
			}
			if tt.wantErrContain != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContain)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPath, got)
		})
	}
}

func TestDetectProjectRoot(t *testing.T) {
	t.Parallel()

	t.Run("finds .git directory", func(t *testing.T) {
		t.Parallel()
		projectRoot := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(projectRoot, ".git"), 0700))

		subDir := filepath.Join(projectRoot, "src", "pkg")
		require.NoError(t, os.MkdirAll(subDir, 0700))

		got, err := DetectProjectRoot(subDir)
		require.NoError(t, err)
		assert.Equal(t, projectRoot, got)
	})

	t.Run("finds .git file (worktree)", func(t *testing.T) {
		t.Parallel()
		projectRoot := t.TempDir()
		// Worktrees have a .git file pointing to the real .git dir
		require.NoError(t, os.WriteFile(
			filepath.Join(projectRoot, ".git"),
			[]byte("gitdir: /some/other/.git/worktrees/foo"),
			0600,
		))

		got, err := DetectProjectRoot(projectRoot)
		require.NoError(t, err)
		assert.Equal(t, projectRoot, got)
	})

	t.Run("returns error when no .git found", func(t *testing.T) {
		t.Parallel()
		noGitDir := t.TempDir()

		_, err := DetectProjectRoot(noGitDir)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrProjectRootNotFound))
	})
}

func TestLookupClientAppConfig(t *testing.T) {
	t.Parallel()
	cm := newTestSkillManager()

	tests := []struct {
		name       string
		clientType ClientApp
		wantNil    bool
		wantType   ClientApp
	}{
		{name: "finds existing client", clientType: ClaudeCode, wantNil: false, wantType: ClaudeCode},
		{name: "finds another client", clientType: Codex, wantNil: false, wantType: Codex},
		{name: "returns nil for unknown client", clientType: ClientApp("nonexistent"), wantNil: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := cm.lookupClientAppConfig(tt.clientType)
			if tt.wantNil {
				assert.Nil(t, cfg)
			} else {
				require.NotNil(t, cfg)
				assert.Equal(t, tt.wantType, cfg.ClientType)
			}
		})
	}

	t.Run("returns pointer to slice element not a copy", func(t *testing.T) {
		t.Parallel()
		cfg := cm.lookupClientAppConfig(ClaudeCode)
		require.NotNil(t, cfg)
		// Verify we got a pointer into the actual slice, not a copy
		assert.Same(t, &cm.clientIntegrations[0], cfg)
	})
}

func TestPlatformPrefixKeysAreValid(t *testing.T) {
	t.Parallel()

	validPlatforms := map[Platform]bool{
		PlatformLinux:   true,
		PlatformDarwin:  true,
		PlatformWindows: true,
	}

	// Verify all PlatformPrefix and SkillsPlatformPrefix keys in
	// supportedClientIntegrations use valid Platform constants.
	for _, cfg := range supportedClientIntegrations {
		for platform := range cfg.PlatformPrefix {
			assert.True(t, validPlatforms[platform],
				"client %s has unknown PlatformPrefix key %q", cfg.ClientType, platform)
		}
		for platform := range cfg.SkillsPlatformPrefix {
			assert.True(t, validPlatforms[platform],
				"client %s has unknown SkillsPlatformPrefix key %q", cfg.ClientType, platform)
		}
	}
}
