// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/skills"
)

func TestUpgradeExitError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		outcomes      []skills.UpgradeOutcome
		preview       bool
		failOnChanges bool
		wantCode      int
	}{
		{
			name:     "all up to date",
			outcomes: []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusUpToDate}},
			wantCode: 0,
		},
		{
			name:     "upgraded is not a failure",
			outcomes: []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusUpgraded}},
			wantCode: 0,
		},
		{
			name:     "not upgradable is not a failure",
			outcomes: []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusNotUpgradable}},
			wantCode: 0,
		},
		{
			name:     "ref change blocked is a policy rejection",
			outcomes: []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusRefChangeBlocked}},
			wantCode: ExitCodePolicyRejection,
		},
		{
			name:     "ref change blocked during preview is not a policy rejection",
			outcomes: []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusRefChangeBlocked}},
			preview:  true,
			wantCode: 0,
		},
		{
			name:     "failed outcome is a partial failure",
			outcomes: []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusFailed}},
			wantCode: ExitCodePartialFailure,
		},
		{
			name: "failure takes precedence over ref-change-blocked",
			outcomes: []skills.UpgradeOutcome{
				{Name: "a", Status: skills.UpgradeStatusRefChangeBlocked},
				{Name: "b", Status: skills.UpgradeStatusFailed},
			},
			wantCode: ExitCodePartialFailure,
		},
		{
			name: "failure still fails even during preview",
			outcomes: []skills.UpgradeOutcome{
				{Name: "a", Status: skills.UpgradeStatusFailed},
			},
			preview:  true,
			wantCode: ExitCodePartialFailure,
		},
		{
			name:          "fail-on-changes with pending upgrade is a check failure",
			outcomes:      []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusUpgraded}},
			failOnChanges: true,
			wantCode:      ExitCodeCheckFailure,
		},
		{
			name:          "fail-on-changes with a blocked ref change is a check failure",
			outcomes:      []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusRefChangeBlocked}},
			failOnChanges: true,
			wantCode:      ExitCodeCheckFailure,
		},
		{
			name:          "fail-on-changes with everything current exits clean",
			outcomes:      []skills.UpgradeOutcome{{Name: "a", Status: skills.UpgradeStatusUpToDate}},
			failOnChanges: true,
			wantCode:      0,
		},
		{
			// The exit-code inversion from the panel review: a genuine
			// resolution failure during the CI gate must exit 3, not be
			// silently downgraded to "lock is stale" (exit 2).
			name: "fail-on-changes with a genuine failure is a partial failure, not a check failure",
			outcomes: []skills.UpgradeOutcome{
				{Name: "a", Status: skills.UpgradeStatusUpgraded},
				{Name: "b", Status: skills.UpgradeStatusFailed},
			},
			failOnChanges: true,
			wantCode:      ExitCodePartialFailure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := upgradeExitError(&skills.UpgradeResult{Outcomes: tt.outcomes}, tt.preview, tt.failOnChanges)
			assert.Equal(t, tt.wantCode, ExitCodeFromError(err))
		})
	}
}

func TestPrintUpgradeResultJSON(t *testing.T) {
	t.Parallel()
	err := printUpgradeResult(&skills.UpgradeResult{
		Outcomes: []skills.UpgradeOutcome{{Name: "my-skill", Status: skills.UpgradeStatusUpgraded}},
	}, FormatJSON, false)
	require.NoError(t, err)
}

func TestPrintUpgradeResultTextNoOutcomes(t *testing.T) {
	t.Parallel()
	err := printUpgradeResult(&skills.UpgradeResult{}, FormatText, false)
	require.NoError(t, err)
}

func TestPrintUpgradeResultTextEveryStatus(t *testing.T) {
	t.Parallel()
	err := printUpgradeResult(&skills.UpgradeResult{Outcomes: []skills.UpgradeOutcome{
		{Name: "upgraded-skill", Status: skills.UpgradeStatusUpgraded, OldDigest: "old", NewDigest: "new"},
		{Name: "current-skill", Status: skills.UpgradeStatusUpToDate},
		{Name: "pinned-skill", Status: skills.UpgradeStatusNotUpgradable},
		{Name: "blocked-skill", Status: skills.UpgradeStatusRefChangeBlocked, NewResolvedReference: "new-ref"},
		{Name: "failed-skill", Status: skills.UpgradeStatusFailed, Reason: skills.FailureReasonUnknown, Error: "boom"},
	}}, FormatText, true)
	require.NoError(t, err)
}
