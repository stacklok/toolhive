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
			name:     "trust-only update is not a failure",
			outcomes: []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusTrustUpdated}},
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
			name:          "trust-only update counts as would-change under fail-on-changes",
			outcomes:      []plugins.UpgradeOutcome{{Name: "a", Status: plugins.UpgradeStatusTrustUpdated}},
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

// TestPluginUpgradeTrustFlags pins the consent and replacement-key flags and
// their bindings to the variables used to build plugins.UpgradeOptions.
//
//nolint:paralleltest // mutates the command's package-level flag state
func TestPluginUpgradeTrustFlags(t *testing.T) {
	signerFlag := aiPluginUpgradeCmd.Flags().Lookup("allow-signer-change")
	require.NotNil(t, signerFlag, "the status message promises --allow-signer-change")
	assert.Equal(t, "false", signerFlag.DefValue, "a signer rotation is never permitted by default")
	publicKeyFlag := aiPluginUpgradeCmd.Flags().Lookup("public-key")
	require.NotNil(t, publicKeyFlag, "thv ai-plugin upgrade must expose --public-key")
	assert.Empty(t, publicKeyFlag.DefValue)

	t.Cleanup(func() {
		aiPluginUpgradeAllowSignerChange = false
		aiPluginUpgradePublicKey = ""
		require.NoError(t, signerFlag.Value.Set("false"))
		require.NoError(t, publicKeyFlag.Value.Set(""))
		signerFlag.Changed = false
		publicKeyFlag.Changed = false
	})
	require.NoError(t, aiPluginUpgradeCmd.Flags().Set("allow-signer-change", "true"))
	require.NoError(t, aiPluginUpgradeCmd.Flags().Set("public-key", "cosign.pub"))
	assert.True(t, aiPluginUpgradeAllowSignerChange,
		"the flag must bind to the variable threaded into plugins.UpgradeOptions")
	assert.Equal(t, "cosign.pub", aiPluginUpgradePublicKey,
		"the key flag must bind to the path upgrade reads and encodes before the HTTP request")
}

// TestPrintPluginUpgradeResultSignerRendering pins the distinction the
// blocked-outcome line draws between a candidate that changed signer and one
// that has no signer at all. The renderer infers "unsigned" from an absent
// NewSignerIdentity, so that field is load-bearing: a keyless candidate that
// reaches this point unnamed is printed as unsigned, telling the user the
// artifact carries no signature when it carries one they have not approved.
//
//nolint:paralleltest // Test captures os.Stdout which cannot be done in parallel
func TestPrintPluginUpgradeResultSignerRendering(t *testing.T) {
	tests := []struct {
		name       string
		outcome    plugins.UpgradeOutcome
		wantOutput string
	}{
		{
			name: "a key-to-keyless move names the identity it moved to",
			outcome: plugins.UpgradeOutcome{
				Name:              "keyed-plugin",
				Status:            plugins.UpgradeStatusSignerChangeBlocked,
				NewSignerIdentity: "ci@example.com",
			},
			wantOutput: "keyed-plugin: signer change blocked (candidate is ci@example.com;" +
				" use --allow-signer-change)\n",
		},
		{
			name: "only a candidate with no identity is called unsigned",
			outcome: plugins.UpgradeOutcome{
				Name:   "keyless-plugin",
				Status: plugins.UpgradeStatusSignerChangeBlocked,
			},
			wantOutput: "keyless-plugin: signer change blocked (candidate is unsigned;" +
				" use --allow-signer-change)\n",
		},
		{
			// The keyed guard routes an unsigned candidate here instead, so
			// the flag above is never suggested for one.
			name: "an unsigned candidate under a pinned key reports the rejection",
			outcome: plugins.UpgradeOutcome{
				Name:   "keyed-plugin",
				Status: plugins.UpgradeStatusFailed,
				Reason: plugins.FailureReasonUnsignedRejected,
				Error:  "candidate is unsigned, and this entry is pinned to a cosign public key",
			},
			wantOutput: "keyed-plugin: failed [unsigned-rejected]: candidate is unsigned," +
				" and this entry is pinned to a cosign public key\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := captureStdout(t, func() {
				require.NoError(t, printPluginUpgradeResult(
					&plugins.UpgradeResult{Outcomes: []plugins.UpgradeOutcome{tc.outcome}},
					FormatText, false))
			})
			assert.Equal(t, tc.wantOutput, output)
		})
	}
}

//nolint:paralleltest // captures os.Stdout
func TestPrintPluginUpgradeResultTrustChanges(t *testing.T) {
	tests := []struct {
		name     string
		outcome  plugins.UpgradeOutcome
		planOnly bool
		want     string
	}{
		{
			name: "applied trust-only update",
			outcome: plugins.UpgradeOutcome{
				Name: "keyed-plugin", Status: plugins.UpgradeStatusTrustUpdated, OldDigest: "sha256:same",
			},
			want: "keyed-plugin: updated trust metadata (content remains at sha256:same; verification material refreshed)\n",
		},
		{
			name: "preview trust-only update",
			outcome: plugins.UpgradeOutcome{
				Name: "keyed-plugin", Status: plugins.UpgradeStatusTrustUpdated, OldDigest: "sha256:same",
			},
			planOnly: true,
			want:     "keyed-plugin: would update trust metadata (content remains at sha256:same; verification material refreshed)\n",
		},
		{
			name: "content and trust update",
			outcome: plugins.UpgradeOutcome{
				Name: "keyed-plugin", Status: plugins.UpgradeStatusUpgraded,
				OldDigest: "sha256:old", NewDigest: "sha256:new", TrustAnchorChanged: true,
			},
			want: "keyed-plugin: upgraded sha256:old -> sha256:new (trust anchor changed)\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := captureStdout(t, func() {
				require.NoError(t, printPluginUpgradeResult(
					&plugins.UpgradeResult{Outcomes: []plugins.UpgradeOutcome{tc.outcome}}, FormatText, tc.planOnly))
			})
			assert.Equal(t, tc.want, got)
		})
	}
}
