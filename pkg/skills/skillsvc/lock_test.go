// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
	"github.com/stacklok/toolhive/pkg/storage"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestInstallRecordsProjectScopeLockEntry(t *testing.T) {
	t.Parallel()

	layerData := makeLayerData(t)
	projectRoot := makeProjectRoot(t)

	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)
	pr := skillsmocks.NewMockPathResolver(ctrl)

	targetDir := filepath.Join(projectRoot, ".claude", "skills", "my-skill")
	pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
	pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeProject, projectRoot).Return(targetDir, nil)
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).
		Return(skills.InstalledSkill{}, storage.ErrNotFound)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	svc := New(store, withTestVerifier(), WithPathResolver(pr))
	result, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:          "my-skill",
		LayerData:     layerData,
		Digest:        "sha256:abcdef0123456789",
		Reference:     "ghcr.io/org/my-skill:v1",
		Version:       "1.0.0",
		Scope:         skills.ScopeProject,
		ProjectRoot:   projectRoot,
		AllowUnsigned: true,
	})
	require.NoError(t, err)
	assert.Equal(t, "my-skill", result.Skill.Metadata.Name)

	lf, err := lockfile.Load(projectRoot)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	entry := lf.Skills[0]
	assert.Equal(t, "my-skill", entry.Name)
	assert.Equal(t, "1.0.0", entry.Version)
	// Source is exactly what the caller passed as Name — what "thv skill
	// upgrade" later re-resolves — not the OCI reference it happened to install.
	assert.Equal(t, "my-skill", entry.Source)
	assert.Equal(t, "ghcr.io/org/my-skill:v1", entry.ResolvedReference)
	assert.Equal(t, "sha256:abcdef0123456789", entry.Digest)
}

func TestInstallUserScopeDoesNotWriteLockFile(t *testing.T) {
	t.Parallel()

	layerData := makeLayerData(t)
	baseDir := tempDir(t)

	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)
	pr := skillsmocks.NewMockPathResolver(ctrl)

	targetDir := filepath.Join(baseDir, "my-skill")
	pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
	pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeUser, "").Return(targetDir, nil)
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeUser, "").Return(skills.InstalledSkill{}, storage.ErrNotFound)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	svc := New(store, WithPathResolver(pr))
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name:      "my-skill",
		LayerData: layerData,
		Digest:    "sha256:abcdef0123456789",
	})
	require.NoError(t, err)

	_, statErr := os.Stat(lockfile.Path(baseDir))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestUninstallRemovesProjectScopeLockEntry(t *testing.T) {
	t.Parallel()

	projectRoot := makeProjectRoot(t)
	require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
		Name:   "my-skill",
		Source: "my-skill",
		Digest: "sha256:abcdef0123456789",
	}))
	require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
		Name:   "other-skill",
		Source: "other-skill",
		Digest: "sha256:1234567890abcdef",
	}))

	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)

	existing := skills.InstalledSkill{
		Metadata:    skills.SkillMetadata{Name: "my-skill"},
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
	}
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).Return(existing, nil)
	store.EXPECT().Delete(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).Return(nil)

	svc := New(store)
	err := svc.Uninstall(t.Context(), skills.UninstallOptions{
		Name:        "my-skill",
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
	})
	require.NoError(t, err)

	lf, err := lockfile.Load(projectRoot)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "other-skill", lf.Skills[0].Name)
}

func TestInstallInternalDoesNotTouchLockFile(t *testing.T) {
	t.Parallel()

	layerData := makeLayerData(t)
	projectRoot := makeProjectRoot(t)

	ctrl := gomock.NewController(t)
	store := storemocks.NewMockSkillStore(ctrl)
	pr := skillsmocks.NewMockPathResolver(ctrl)

	targetDir := filepath.Join(projectRoot, ".claude", "skills", "my-skill")
	pr.EXPECT().ListSkillSupportingClients().Return([]string{"claude-code"})
	pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeProject, projectRoot).Return(targetDir, nil)
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).
		Return(skills.InstalledSkill{}, storage.ErrNotFound)
	store.EXPECT().Create(gomock.Any(), gomock.Any()).Return(nil)

	svc := New(store, withTestVerifier(), WithPathResolver(pr)).(*service)
	_, err := svc.installInternal(context.Background(), skills.InstallOptions{
		Name:          "my-skill",
		LayerData:     layerData,
		Digest:        "sha256:abcdef0123456789",
		Scope:         skills.ScopeProject,
		ProjectRoot:   projectRoot,
		AllowUnsigned: true,
	})
	require.NoError(t, err)

	_, statErr := os.Stat(lockfile.Path(projectRoot))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}
