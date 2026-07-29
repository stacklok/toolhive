// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/lockfile"
	"github.com/stacklok/toolhive/pkg/skills/verifier"
	verifiermocks "github.com/stacklok/toolhive/pkg/skills/verifier/mocks"
)

// otherSignerResult is a verification result from a different identity than
// signedResult's.
func otherSignerResult() *verifier.Result {
	r := signedResult()
	r.SignerIdentity = "/.github/workflows/other.yml"
	return r
}

// signerChangeFixture installs a signed skill, then republishes newer
// content at the same source and returns a service whose verifier reports
// the candidate as signed by candidate() (or unsigned when candidate
// returns nil, err).
func signerChangeFixture(
	t *testing.T,
	candidate func() (*verifier.Result, error),
) (skills.SkillService, string) {
	t.Helper()
	gr, fx := newGitResolverMock(t)
	fx.register("guarded-skill", gitSkill("guarded-skill"))

	installs := 0
	mv := verifiermocks.NewMockVerifier(gomock.NewController(t))
	mv.EXPECT().VerifyGit(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().
		DoAndReturn(func(_ any, _, _ []byte, expected *lockfile.Provenance) (*verifier.Result, error) {
			installs++
			if installs == 1 {
				return signedResult(), nil // initial install (TOFU)
			}
			result, err := candidate()
			if err != nil {
				return nil, err
			}
			if expected != nil && expected.SignerIdentity != result.SignerIdentity {
				return nil, verifier.ErrSignerMismatch
			}
			return result, nil
		})
	mv.EXPECT().VerifyBundleOffline(gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes().Return(nil)

	svc, projectRoot := newLockTestService(t, gr, WithVerifier(mv))
	ref, _ := gitRef("guarded-skill")
	_, err := svc.Install(t.Context(), skills.InstallOptions{
		Name: ref, Scope: skills.ScopeProject, ProjectRoot: projectRoot, Clients: []string{"claude-code"},
	})
	require.NoError(t, err)

	// Republish newer content at the same source so an upgrade is planned.
	fx.register("guarded-skill", gitSkillVersion("guarded-skill"))
	return svc, projectRoot
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_SignerChangeBlocked(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status)
	assert.Equal(t, "/.github/workflows/other.yml", result.Outcomes[0].NewSignerIdentity)

	// Nothing installed, lock unchanged.
	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("guarded-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_UnsignedCandidateBlockedAgainstSignedEntry(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return nil, verifier.ErrUnsigned
	})

	result, err := svc.(*service).Upgrade(t.Context(), skills.UpgradeOptions{ProjectRoot: projectRoot}) //nolint:forcetypeassert
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status)
	assert.Empty(t, result.Outcomes[0].NewSignerIdentity, "an unsigned candidate has no identity to report")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_AllowSignerChangeRecordsNewIdentity(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	result, err := svc.(*service).Upgrade(t.Context(), //nolint:forcetypeassert
		skills.UpgradeOptions{ProjectRoot: projectRoot, AllowSignerChange: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusUpgraded, result.Outcomes[0].Status)

	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("guarded-skill")
	require.True(t, ok)
	require.NotNil(t, entry.Provenance)
	assert.Equal(t, "/.github/workflows/other.yml", entry.Provenance.SignerIdentity,
		"the explicit override must re-record the new identity")
}

//nolint:paralleltest // uses t.Setenv via newLockTestService, incompatible with t.Parallel
func TestUpgrade_SignerChangePreviewParity(t *testing.T) {
	svc, projectRoot := signerChangeFixture(t, func() (*verifier.Result, error) {
		return otherSignerResult(), nil
	})

	result, err := svc.(*service).Upgrade(t.Context(), //nolint:forcetypeassert
		skills.UpgradeOptions{ProjectRoot: projectRoot, Preview: true})
	require.NoError(t, err)
	require.Len(t, result.Outcomes, 1)
	assert.Equal(t, skills.UpgradeStatusSignerChangeBlocked, result.Outcomes[0].Status,
		"preview must report the same signer-change block as apply")

	// Preview installed nothing and rewrote nothing.
	lf := readLockfile(t, projectRoot)
	entry, ok := lf.Get("guarded-skill")
	require.True(t, ok)
	assert.Equal(t, testSignerIdentity, entry.Provenance.SignerIdentity)
}
