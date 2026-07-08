// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestSyncInstallsDriftedReportsAlreadyCurrentAndUnmanaged(t *testing.T) {
	t.Parallel()
	projectRoot := makeProjectRoot(t)

	ociStore, err := ociskills.NewStore(tempDir(t))
	require.NoError(t, err)
	indexDigest := buildTestArtifact(t, ociStore, "my-skill", "1.0.0")

	pinnedRef, err := pinOCIReference("ghcr.io/org/my-skill:v1", indexDigest.String())
	require.NoError(t, err)

	require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
		Name:              "my-skill",
		Version:           "1.0.0",
		Source:            "my-skill",
		ResolvedReference: "ghcr.io/org/my-skill:v1",
		Digest:            indexDigest.String(),
		Unsigned:          true,
	}))
	require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
		Name:              "already-current",
		Source:            "already-current",
		ResolvedReference: "ghcr.io/org/already-current:v1",
		Digest:            "sha256:" + strings.Repeat("d", 64),
	}))

	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Pull(gomock.Any(), ociStore, pinnedRef).Return(indexDigest, nil)

	store := storemocks.NewMockSkillStore(ctrl)
	// Called once by Sync's own drift check, and once more inside the normal
	// install flow that Sync delegates to for the actual (re)install.
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).
		Return(skills.InstalledSkill{}, storage.ErrNotFound).Times(2)
	store.EXPECT().Get(gomock.Any(), "already-current", skills.ScopeProject, projectRoot).
		Return(skills.InstalledSkill{
			Metadata: skills.SkillMetadata{Name: "already-current"},
			Digest:   "sha256:" + strings.Repeat("d", 64),
		}, nil)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)
	store.EXPECT().List(gomock.Any(), storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot}).
		Return([]skills.InstalledSkill{
			{Metadata: skills.SkillMetadata{Name: "my-skill"}},
			{Metadata: skills.SkillMetadata{Name: "already-current"}},
			{Metadata: skills.SkillMetadata{Name: "extra-skill"}},
		}, nil)

	pr := skillsmocks.NewMockPathResolver(ctrl)
	targetDir := filepath.Join(tempDir(t), "installed", "my-skill")
	pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeProject, projectRoot).Return(targetDir, nil)

	svc := New(store, withTestVerifier(), WithPathResolver(pr), WithRegistryClient(reg), WithOCIStore(ociStore))
	lockSvc := svc.(skills.SkillLockService)
	result, err := lockSvc.Sync(t.Context(), skills.SyncOptions{
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"my-skill"}, result.Installed)
	assert.Equal(t, []string{"already-current"}, result.AlreadyCurrent)
	assert.Equal(t, []string{"extra-skill"}, result.NeverManaged)
	assert.Empty(t, result.Pruned)
	assert.Empty(t, result.Failed)

	// A sync that only restores pinned digests must never rewrite the lock file.
	lf, err := lockfile.Load(projectRoot)
	require.NoError(t, err)
	entry, ok := lf.Get("my-skill")
	require.True(t, ok)
	assert.Equal(t, "my-skill", entry.Source)
}

func TestSyncPrunesUnmanagedSkills(t *testing.T) {
	t.Parallel()
	projectRoot := makeProjectRoot(t)

	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)
	store.EXPECT().List(gomock.Any(), storage.ListFilter{Scope: skills.ScopeProject, ProjectRoot: projectRoot}).
		Return([]skills.InstalledSkill{
			{Metadata: skills.SkillMetadata{Name: "extra-skill"}, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Managed: true},
		}, nil)
	store.EXPECT().Get(gomock.Any(), "extra-skill", skills.ScopeProject, projectRoot).
		Return(skills.InstalledSkill{
			Metadata:    skills.SkillMetadata{Name: "extra-skill"},
			Scope:       skills.ScopeProject,
			ProjectRoot: projectRoot,
		}, nil)
	store.EXPECT().Delete(gomock.Any(), "extra-skill", skills.ScopeProject, projectRoot).Return(nil)

	svc := New(store)
	lockSvc := svc.(skills.SkillLockService)
	result, err := lockSvc.Sync(t.Context(), skills.SyncOptions{ProjectRoot: projectRoot, Prune: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"extra-skill"}, result.Pruned)
	assert.Empty(t, result.NeverManaged)
	assert.Equal(t, []string{"extra-skill"}, result.RemovedFromLock)
}

func TestSyncRejectsInvalidProjectRoot(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)

	svc := New(store)
	lockSvc := svc.(skills.SkillLockService)
	_, err := lockSvc.Sync(t.Context(), skills.SyncOptions{ProjectRoot: "relative/path"})
	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, httperr.Code(err))
}
