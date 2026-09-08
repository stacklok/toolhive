// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/plugins"
)

func TestPluginUpgradeExitError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		outcomes      []plugins.UpgradeOutcome
		preview       bool
		failOnChanges bool
		wantCode      int
	}{
		{
			name:     "all up to date",
			outcomes: []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusUpToDate}},
			wantCode: 0,
		},
		{
			name:     "signer change blocked is a policy rejection",
			outcomes: []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusSignerChangeBlocked}},
			wantCode: ExitCodePolicyRejection,
		},
		{
			name:     "signer change blocked in preview is informational",
			outcomes: []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusSignerChangeBlocked}},
			preview:  true,
			wantCode: 0,
		},
		{
			name:          "signer change blocked counts as would-change under fail-on-changes",
			outcomes:      []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusSignerChangeBlocked}},
			failOnChanges: true,
			wantCode:      ExitCodeCheckFailure,
		},
		{
			// A genuine failure must never be masked by a guard doing its job.
			name: "a failure outranks a signer-change block",
			outcomes: []plugins.UpgradeOutcome{
				{Name: "a", Status: plugins.UpgradeStatusSignerChangeBlocked},
				{Name: "b", Status: plugins.UpgradeStatusFailed},
			},
			wantCode: ExitCodePartialFailure,
		},
		{
			name:     "ref change blocked is still a policy rejection",
			outcomes: []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusRefChangeBlocked}},
			wantCode: ExitCodePolicyRejection,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := pluginUpgradeExitError(
				&plugins.UpgradeResult{Outcomes: tc.outcomes}, tc.preview, tc.failOnChanges)
			if tc.wantCode == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, ExitCodeFromError(err))
		})
	}
}

// TestPluginUpgradeAllowSignerChangeFlag pins the flag name the blocked-status
// message tells users to pass, and its binding to the variable the upgrade
// options are built from.
//
//nolint:paralleltest // mutates the command's package-level flag state
func TestPluginUpgradeAllowSignerChangeFlag(t *testing.T) {
	flag := aiPluginUpgradeCmd.Flags().Lookup("allow-signer-change")
	require.NotNil(t, flag, "the status message promises --allow-signer-change")
	assert.Equal(t, "false", flag.DefValue, "a signer rotation is never permitted by default")

	t.Cleanup(func() {
		aiPluginUpgradeAllowSignerChange = false
		require.NoError(t, flag.Value.Set("false"))
		flag.Changed = false
	})
	require.NoError(t, aiPluginUpgradeCmd.Flags().Set("allow-signer-change", "true"))
	assert.True(t, aiPluginUpgradeAllowSignerChange,
		"the flag must bind to the variable threaded into plugins.UpgradeOptions")
}
