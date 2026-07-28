// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"fmt"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	ocimocks "github.com/stacklok/toolhive-core/oci/skills/mocks"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	regmocks "github.com/stacklok/toolhive/pkg/registry/mocks"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	storemocks "github.com/stacklok/toolhive/pkg/storage/mocks"
)

// newOCIUpgradeService builds a service with a mocked OCI registry client, a
// real (temp) OCI store, and an optional registry lookup — the dependencies
// planUpgrade's OCI resolution path touches. The skill store is a mock with
// no expectations: planning never writes.
func newOCIUpgradeService(
	t *testing.T, reg ociskills.RegistryClient, lookup *regmocks.MockProvider,
) *service {
	t.Helper()
	ctrl := gomock.NewController(t)
	ociStore, err := ociskills.NewStore(tempDir(t))
	require.NoError(t, err)

	opts := []Option{WithRegistryClient(reg), WithOCIStore(ociStore)}
	if lookup != nil {
		opts = append(opts, WithSkillLookup(lookup))
	}
	svc := New(storemocks.NewMockSkillStore(ctrl), opts...)
	return svc.(*service) //nolint:forcetypeassert // white-box test in the same package
}

func ociTestDigest(seed byte) string {
	const alphabet = "0123456789abcdef"
	b := make([]byte, 64)
	for i := range b {
		b[i] = alphabet[(i+int(seed))%len(alphabet)]
	}
	return "sha256:" + string(b)
}

// TestPlanUpgrade_OCITagLessSourceUpgrades pins the resolvedReference
// qualification contract: install records qualifiedOCIRef (implicit
// ":latest" made explicit), so upgrade's resolution must produce the same
// form. Before the fix, resolveOCILatest returned ref.String() — the
// unqualified form — so every digest change on a tag-less source was
// misreported as a blocked reference change instead of an upgrade.
func TestPlanUpgrade_OCITagLessSourceUpgrades(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/skill").
		Return(godigest.Digest(ociTestDigest(2)), nil)
	svc := newOCIUpgradeService(t, reg, nil)

	entry := lockfile.Entry{
		Name:              "my-skill",
		Source:            "ghcr.io/org/skill", // tag-less: the most common upgrade case
		ResolvedReference: "ghcr.io/org/skill:latest",
		Digest:            ociTestDigest(1),
	}
	plan := svc.planUpgrade(t.Context(), skills.UpgradeOptions{}, entry)

	assert.Equal(t, skills.UpgradeStatusUpgraded, plan.outcome.Status,
		"a moved digest on a tag-less source is an upgrade, not a blocked ref change")
	assert.Equal(t, ociTestDigest(2), plan.outcome.NewDigest)
	assert.Equal(t, "ghcr.io/org/skill:latest", plan.resolvedRef,
		"upgrade must resolve to the same qualified form install records")
}

func TestPlanUpgrade_OCITaggedSourceUpToDate(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/skill:v1").
		Return(godigest.Digest(ociTestDigest(1)), nil)
	svc := newOCIUpgradeService(t, reg, nil)

	entry := lockfile.Entry{
		Name:              "my-skill",
		Source:            "ghcr.io/org/skill:v1",
		ResolvedReference: "ghcr.io/org/skill:v1",
		Digest:            ociTestDigest(1),
	}
	plan := svc.planUpgrade(t.Context(), skills.UpgradeOptions{}, entry)
	assert.Equal(t, skills.UpgradeStatusUpToDate, plan.outcome.Status)
}

// TestPlanUpgrade_OCIRefChangeGuard exercises the guard on a genuine
// reference change (the recorded resolvedReference no longer matches what
// the source resolves to): blocked by default, permitted with
// --allow-ref-change.
func TestPlanUpgrade_OCIRefChangeGuard(t *testing.T) {
	t.Parallel()

	entry := lockfile.Entry{
		Name:              "my-skill",
		Source:            "ghcr.io/org/skill",
		ResolvedReference: "ghcr.io/org/old-location:latest", // recorded before the repoint
		Digest:            ociTestDigest(1),
	}

	t.Run("blocked without allow-ref-change", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/skill").
			Return(godigest.Digest(ociTestDigest(2)), nil)
		svc := newOCIUpgradeService(t, reg, nil)

		plan := svc.planUpgrade(t.Context(), skills.UpgradeOptions{}, entry)
		assert.Equal(t, skills.UpgradeStatusRefChangeBlocked, plan.outcome.Status)
		assert.Equal(t, "ghcr.io/org/skill:latest", plan.outcome.NewResolvedReference)
		assert.Empty(t, plan.pinnedRef, "a blocked plan must not carry anything to install")
	})

	t.Run("permitted with allow-ref-change", func(t *testing.T) {
		t.Parallel()
		ctrl := gomock.NewController(t)
		reg := ocimocks.NewMockRegistryClient(ctrl)
		reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/org/skill").
			Return(godigest.Digest(ociTestDigest(2)), nil)
		svc := newOCIUpgradeService(t, reg, nil)

		plan := svc.planUpgrade(t.Context(), skills.UpgradeOptions{AllowRefChange: true}, entry)
		assert.Equal(t, skills.UpgradeStatusUpgraded, plan.outcome.Status)
		assert.Equal(t, "ghcr.io/org/skill:latest", plan.outcome.NewResolvedReference)
		assert.NotEmpty(t, plan.pinnedRef)
	})
}

// TestPlanUpgrade_RegistryFallbackSourceUpgrades covers the dropped branch:
// a skill originally installed through Install's OCI-pull -> registry-
// catalogue fallback has an ambiguous "namespace/name" source that fails a
// direct pull. Without mirroring the fallback, such a skill installs
// cleanly but can never be upgraded — its resolution fails exactly as the
// direct pull did at install time.
func TestPlanUpgrade_RegistryFallbackSourceUpgrades(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	reg := ocimocks.NewMockRegistryClient(ctrl)
	// The direct pull of the ambiguous source fails, as it did at install.
	reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "io.github.test/my-skill").
		Return(godigest.Digest(""), fmt.Errorf("name unknown"))
	// The registry catalogue resolves it to a concrete OCI reference.
	reg.EXPECT().Pull(gomock.Any(), gomock.Any(), "ghcr.io/test/my-skill:v2").
		Return(godigest.Digest(ociTestDigest(3)), nil)
	lookup := regmocks.NewMockProvider(ctrl)
	lookup.EXPECT().SearchSkills("my-skill").Return([]regtypes.Skill{
		{
			Namespace: "io.github.test",
			Name:      "my-skill",
			Packages: []regtypes.SkillPackage{
				{RegistryType: "oci", Identifier: "ghcr.io/test/my-skill:v2"},
			},
		},
	}, nil)
	svc := newOCIUpgradeService(t, reg, lookup)

	entry := lockfile.Entry{
		Name:              "my-skill",
		Source:            "io.github.test/my-skill", // registry catalogue name, ambiguous OCI shape
		ResolvedReference: "ghcr.io/test/my-skill:v2",
		Digest:            ociTestDigest(1),
	}
	plan := svc.planUpgrade(t.Context(), skills.UpgradeOptions{}, entry)

	assert.Equal(t, skills.UpgradeStatusUpgraded, plan.outcome.Status,
		"a registry-fallback-installed skill must be upgradable through the same fallback")
	assert.Equal(t, ociTestDigest(3), plan.outcome.NewDigest)
}
