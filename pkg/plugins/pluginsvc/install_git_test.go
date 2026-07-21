// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociartifact "github.com/stacklok/toolhive-core/oci/artifact"
	"github.com/stacklok/toolhive/pkg/git"
	"github.com/stacklok/toolhive/pkg/plugins"
	plugmocks "github.com/stacklok/toolhive/pkg/plugins/mocks"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestParseGitReference_RefAndSubdir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw       string
		wantURL   string
		wantRef   string
		wantPath  string
		wantError bool
	}{
		{raw: "git://github.com/org/repo", wantURL: "https://github.com/org/repo"},
		{raw: "git://github.com/org/repo@v1.0.0", wantRef: "v1.0.0"},
		{raw: "git://github.com/org/repo#plugins/my-plugin", wantPath: "plugins/my-plugin"},
		{raw: "git://github.com/org/repo@main#plugins/my-plugin", wantRef: "main", wantPath: "plugins/my-plugin"},
		{raw: "git://", wantError: true},
		{raw: "git://github.com", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			t.Parallel()
			ref, err := gitresolver.ParseGitReference(tt.raw)
			if tt.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.wantURL != "" {
				assert.Equal(t, tt.wantURL, ref.URL)
			}
			assert.Equal(t, tt.wantRef, ref.Ref)
			assert.Equal(t, tt.wantPath, ref.Path)
		})
	}
}

func TestInstallFromGit_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("no materializers configured returns 500", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "git://github.com/test/my-plugin"})
		require.Error(t, err)
		assert.Equal(t, http.StatusInternalServerError, httperr.Code(err))
	})

	t.Run("malformed git reference returns 400", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		store := storemocks.NewMockPluginStore(ctrl)
		svc := newTestService(WithStore(store),
			WithMaterializers(map[string]plugins.MaterializationAdapter{"claude-code": plugmocks.NewMockMaterializationAdapter(ctrl)}))
		_, err := svc.Install(t.Context(), plugins.InstallOptions{Name: "git://"})
		require.Error(t, err)
		assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
		assert.Contains(t, err.Error(), "invalid git reference")
	})
}

// TestCloneAndCollectPlugin exercises the clone + manifest parse + file
// collection flow against a real on-disk git repository. It bypasses
// ParseGitReference (which rejects localhost/file paths) by calling
// cloneAndCollectPlugin directly with a file://-style GitReference and an
// injected git.NewDefaultGitClient.
func TestCloneAndCollectPlugin(t *testing.T) {
	t.Parallel()

	t.Run("collects plugin tree and parses manifest", func(t *testing.T) {
		t.Parallel()
		repoDir := createPluginTestRepo(t, "")

		s := &service{gitClient: git.NewDefaultGitClient()}
		files, manifest, commitHash, err := s.cloneAndCollectPlugin(t.Context(), &gitresolver.GitReference{URL: repoDir})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", manifest.Name)
		assert.Equal(t, "1.0.0", manifest.Version)
		assert.NotEmpty(t, commitHash)
		// Manifest + a command file are collected.
		assert.GreaterOrEqual(t, len(files), 2)
	})

	t.Run("subdir scopes the collected tree", func(t *testing.T) {
		t.Parallel()
		repoDir := createPluginTestRepo(t, "bundled")

		s := &service{gitClient: git.NewDefaultGitClient()}
		files, manifest, _, err := s.cloneAndCollectPlugin(t.Context(), &gitresolver.GitReference{
			URL:  repoDir,
			Path: "bundled",
		})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", manifest.Name)
		// Files are relative to the subdir; manifest path is .claude-plugin/plugin.json.
		found := false
		for _, f := range files {
			if f.Path == ".claude-plugin/plugin.json" {
				found = true
			}
		}
		assert.True(t, found, "manifest should be collected from the subdir")
	})

	t.Run("missing manifest returns error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		repo, err := gogit.PlainInit(dir, false)
		require.NoError(t, err)
		wt, err := repo.Worktree()
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# no plugin"), 0644))
		_, err = wt.Add(".")
		require.NoError(t, err)
		_, err = wt.Commit("init", &gogit.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e"}})
		require.NoError(t, err)

		s := &service{gitClient: git.NewDefaultGitClient()}
		_, _, _, err = s.cloneAndCollectPlugin(t.Context(), &gitresolver.GitReference{URL: dir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found in plugin directory")
	})

	t.Run("executable file mode is preserved", func(t *testing.T) {
		t.Parallel()
		repoDir := createPluginTestRepoWithExecutable(t, "")

		s := &service{gitClient: git.NewDefaultGitClient()}
		files, manifest, _, err := s.cloneAndCollectPlugin(t.Context(), &gitresolver.GitReference{URL: repoDir})
		require.NoError(t, err)
		assert.Equal(t, "my-plugin", manifest.Name)

		var hookEntry *ociartifact.FileEntry
		for i := range files {
			if files[i].Path == "hooks/preinstall.sh" {
				hookEntry = &files[i]
				break
			}
		}
		require.NotNil(t, hookEntry, "executable hook file should be collected")
		assert.NotZero(t, hookEntry.Mode&0o100,
			"executable bit should be preserved on hook script, got mode %o", hookEntry.Mode)
	})
}

// createPluginTestRepo initializes an on-disk git repo containing a minimal
// plugin tree. When subdir is non-empty, the plugin is placed under that
// subdirectory.
func createPluginTestRepo(t *testing.T, subdir string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)

	pluginRoot := dir
	if subdir != "" {
		pluginRoot = filepath.Join(dir, subdir)
		require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0750))
	} else {
		require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0750))
	}
	manifest := `{"name":"my-plugin","version":"1.0.0","commands":["./commands"]}`
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), []byte(manifest), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, "commands"), 0750))
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, "commands", "hello.md"), []byte("# hello"), 0644))

	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(".")
	require.NoError(t, err)
	_, err = wt.Commit("init", &gogit.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e"}})
	require.NoError(t, err)
	return dir
}

// createPluginTestRepoWithExecutable is like createPluginTestRepo but also
// commits an executable hook script at hooks/preinstall.sh (mode 0755) so the
// exec-bit-preservation path can be exercised.
func createPluginTestRepoWithExecutable(t *testing.T, subdir string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	require.NoError(t, err)

	pluginRoot := dir
	if subdir != "" {
		pluginRoot = filepath.Join(dir, subdir)
	}
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, ".claude-plugin"), 0750))
	manifest := `{"name":"my-plugin","version":"1.0.0"}`
	require.NoError(t, os.WriteFile(filepath.Join(pluginRoot, ".claude-plugin", "plugin.json"), []byte(manifest), 0644))

	// Executable hook script, committed with the executable bit set.
	require.NoError(t, os.MkdirAll(filepath.Join(pluginRoot, "hooks"), 0750))
	hookPath := filepath.Join(pluginRoot, "hooks", "preinstall.sh")
	require.NoError(t, os.WriteFile(hookPath, []byte("#!/bin/sh\necho hi\n"), 0755))

	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(".")
	require.NoError(t, err)
	_, err = wt.Commit("init", &gogit.CommitOptions{Author: &object.Signature{Name: "T", Email: "t@e"}})
	require.NoError(t, err)
	return dir
}

// TestGitNameConsistencyCheck exercises validateGitPluginName, the name/repo
// consistency check for git installs that mirrors the OCI path's check.
func TestGitNameConsistencyCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		manifestName string
		gitRef       *gitresolver.GitReference
		wantErr      bool
		wantCode     int
	}{
		{
			name:         "bare repo mismatch returns 422",
			manifestName: "evil-name",
			gitRef:       &gitresolver.GitReference{URL: "https://github.com/org/my-plugin"},
			wantErr:      true,
			wantCode:     http.StatusUnprocessableEntity,
		},
		{
			name:         "bare repo match passes",
			manifestName: "my-plugin",
			gitRef:       &gitresolver.GitReference{URL: "https://github.com/org/my-plugin"},
		},
		{
			name:         "subdir case matches",
			manifestName: "bundled",
			gitRef:       &gitresolver.GitReference{URL: "https://github.com/org/repo", Path: "bundled"},
		},
		{
			name:         "subdir mismatch returns 422",
			manifestName: "other",
			gitRef:       &gitresolver.GitReference{URL: "https://github.com/org/repo", Path: "bundled"},
			wantErr:      true,
			wantCode:     http.StatusUnprocessableEntity,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateGitPluginName(tt.manifestName, tt.gitRef)
			if tt.wantErr {
				require.Error(t, err)
				assert.Equal(t, tt.wantCode, httperr.Code(err))
				return
			}
			require.NoError(t, err)
		})
	}
}
