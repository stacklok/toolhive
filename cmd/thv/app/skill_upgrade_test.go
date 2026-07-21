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
		name     string
		outcomes []skills.UpgradeOutcome
		preview  bool
		wantCode int
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := upgradeExitError(&skills.UpgradeResult{Outcomes: tt.outcomes}, tt.preview)
			assert.Equal(t, tt.wantCode, ExitCodeFromError(err))
		})
	}
}

func TestPrintUpgradeResultJSON(t *testing.T) {
	t.Parallel()
	err := printUpgradeResult(&skills.UpgradeResult{
		Outcomes: []skills.UpgradeOutcome{{Name: "my-skill", Status: skills.UpgradeStatusUpgraded}},
	}, FormatJSON)
	require.NoError(t, err)
}

func TestPrintUpgradeResultTextNoOutcomes(t *testing.T) {
	t.Parallel()
	err := printUpgradeResult(&skills.UpgradeResult{}, FormatText)
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
	}}, FormatText)
	require.NoError(t, err)
}
