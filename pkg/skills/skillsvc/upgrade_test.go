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
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

func TestUpgradeInstallsNewDigestAndRewritesLockEntry(t *testing.T) {
	t.Parallel()
	projectRoot := makeProjectRoot(t)

	ociStore, err := ociskills.NewStore(tempDir(t))
	require.NoError(t, err)
	newDigest := buildTestArtifact(t, ociStore, "my-skill", "2.0.0")

	require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
		Name:              "my-skill",
		Version:           "1.0.0",
		Source:            "ghcr.io/org/my-skill:v1",
		ResolvedReference: "ghcr.io/org/my-skill:v1",
		Digest:            "sha256:" + strings.Repeat("a", 64),
		Unsigned:          true,
	}))

	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").Return(newDigest, nil)

	store := storemocks.NewMockSkillStore(ctrl)
	existing := skills.InstalledSkill{
		Metadata:    skills.SkillMetadata{Name: "my-skill", Version: "1.0.0"},
		Scope:       skills.ScopeProject,
		ProjectRoot: projectRoot,
		Digest:      "sha256:" + strings.Repeat("a", 64),
		Clients:     []string{"claude-code"},
	}
	store.EXPECT().Get(gomock.Any(), "my-skill", skills.ScopeProject, projectRoot).Return(existing, nil)
	store.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil)

	pr := skillsmocks.NewMockPathResolver(ctrl)
	targetDir := filepath.Join(tempDir(t), "installed", "my-skill")
	pr.EXPECT().GetSkillPath("claude-code", "my-skill", skills.ScopeProject, projectRoot).Return(targetDir, nil)

	svc := New(store, withTestVerifier(), WithPathResolver(pr), WithRegistryClient(reg), WithOCIStore(ociStore))
	lockSvc := svc.(skills.SkillLockService)
	result, err := lockSvc.Upgrade(t.Context(), skills.UpgradeOptions{
		ProjectRoot: projectRoot,
		Clients:     []string{"claude-code"},
	})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	outcome := result.Outcomes[0]
	assert.Equal(t, skills.UpgradeStatusUpgraded, outcome.Status)
	assert.Equal(t, "sha256:"+strings.Repeat("a", 64), outcome.OldDigest)
	assert.Equal(t, newDigest.String(), outcome.NewDigest)

	lf, err := lockfile.Load(projectRoot)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, newDigest.String(), lf.Skills[0].Digest)
	assert.Equal(t, "2.0.0", lf.Skills[0].Version)
	// Source is what "thv skill upgrade" re-resolves next time — it must
	// never be replaced by the concrete reference an upgrade happened to use.
	assert.Equal(t, "ghcr.io/org/my-skill:v1", lf.Skills[0].Source)
}

func TestUpgradeDryRunReportsWithoutInstallingOrRewritingLock(t *testing.T) {
	t.Parallel()
	projectRoot := makeProjectRoot(t)

	ociStore, err := ociskills.NewStore(tempDir(t))
	require.NoError(t, err)
	newDigest := buildTestArtifact(t, ociStore, "my-skill", "2.0.0")

	require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
		Name:              "my-skill",
		Source:            "ghcr.io/org/my-skill:v1",
		ResolvedReference: "ghcr.io/org/my-skill:v1",
		Digest:            "sha256:" + strings.Repeat("a", 64),
	}))

	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Pull(gomock.Any(), ociStore, "ghcr.io/org/my-skill:v1").Return(newDigest, nil)

	// No SkillStore or PathResolver expectations: a dry run must never touch
	// installed state.
	store := storemocks.NewMockSkillStore(ctrl)

	svc := New(store, withTestVerifier(), WithRegistryClient(reg), WithOCIStore(ociStore))
	lockSvc := svc.(skills.SkillLockService)
	result, err := lockSvc.Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot, DryRun: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status)
	assert.Equal(t, newDigest.String(), result.Outcomes[0].NewDigest)

	lf, err := lockfile.Load(projectRoot)
	require.NoError(t, err)
	require.Len(t, lf.Skills, 1)
	assert.Equal(t, "sha256:"+strings.Repeat("a", 64), lf.Skills[0].Digest)
}

func TestUpgradeImmutableSourceReportsNotUpgradable(t *testing.T) {
	t.Parallel()

	fakeCommit := strings.Repeat("a", 40)
	fakeDigest := "sha256:" + strings.Repeat("b", 64)

	tests := []struct {
		name   string
		source string
	}{
		{name: "OCI digest reference", source: "ghcr.io/org/my-skill@" + fakeDigest},
		{name: "git full commit hash", source: "git://github.com/org/my-skill@" + fakeCommit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			projectRoot := makeProjectRoot(t)
			require.NoError(t, lockfile.UpsertEntry(projectRoot, lockfile.Entry{
				Name:   "my-skill",
				Source: tt.source,
				Digest: "sha256:" + strings.Repeat("c", 64),
			}))

			store := storemocks.NewMockSkillStore(gomock.NewController(t))
			svc := New(store)
			lockSvc := svc.(skills.SkillLockService)
			result, err := lockSvc.Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot})
			require.NoError(t, err)
			require.Len(t, result.Outcomes, 1)
			assert.Equal(t, skills.UpgradeStatusNotUpgradable, result.Outcomes[0].Status)
			assert.Equal(t, "sha256:"+strings.Repeat("c", 64), result.Outcomes[0].OldDigest)
		})
	}
}

func TestUpgradeUnknownNameReturns404(t *testing.T) {
	t.Parallel()
	projectRoot := makeProjectRoot(t)

	store := storemocks.NewMockSkillStore(gomock.NewController(t))
	svc := New(store)
	lockSvc := svc.(skills.SkillLockService)
	_, err := lockSvc.Upgrade(t.Context(), skills.UpgradeOptions{
		ProjectRoot: projectRoot,
		Names:       []string{"missing-skill"},
	})
	require.Error(t, err)
	assert.Equal(t, http.StatusNotFound, httperr.Code(err))
}
