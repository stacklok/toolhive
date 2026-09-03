// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/plugins"
)

// TestAIPluginPushSigningFlags pins the signed-by-default publish surface:
// the keyless flags must exist, the opt-out must not be preset (a defaulted
// --no-sign would publish unsigned artifacts silently), and --key must NOT be
// offered — ToolHive cannot verify key-signed artifacts at install time, so
// the flag would only produce uninstallable plugins (#6442). Re-add it in the
// change that makes key verification work.
func TestAIPluginPushSigningFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"identity-token", "no-sign"} {
		flag := aiPluginPushCmd.Flags().Lookup(name)
		require.NotNil(t, flag, "thv ai-plugin push must expose --%s", name)
	}
	assert.Nil(t, aiPluginPushCmd.Flags().Lookup("key"),
		"plugin signing is keyless-only; --key must not be advertised until install can verify it")
	assert.Equal(t, "false", aiPluginPushCmd.Flags().Lookup("no-sign").DefValue,
		"pushing unsigned must always be an explicit choice")
	assert.Empty(t, aiPluginPushCmd.Flags().Lookup("identity-token").DefValue)
}

// TestPrintAIPluginInfoTextTrustStates covers each trust state the info
// command renders. RFC THV-0080 wants the pinned identity visible at read
// time, so a state that silently renders as "no trust block" is a bug.
//
//nolint:paralleltest // captures os.Stdout, which cannot be done in parallel
func TestPrintAIPluginInfoTextTrustStates(t *testing.T) {
	signed := &plugins.ProvenanceInfo{
		SignerIdentity: "/.github/workflows/release.yml",
		CertIssuer:     "https://token.actions.githubusercontent.com",
	}

	tests := []struct {
		name       string
		info       plugins.PluginInfo
		wantLines  []string
		wantAbsent []string
	}{
		{
			name: "signed",
			info: plugins.PluginInfo{Provenance: signed},
			wantLines: []string{
				"Signed by: /.github/workflows/release.yml",
				"Cert issuer: https://token.actions.githubusercontent.com",
			},
			wantAbsent: []string{"provisional", "unsigned"},
		},
		{
			name: "provisional",
			info: plugins.PluginInfo{Provenance: &plugins.ProvenanceInfo{
				SignerIdentity: signed.SignerIdentity,
				CertIssuer:     signed.CertIssuer,
				Provisional:    true,
			}},
			wantLines: []string{"Signed by: /.github/workflows/release.yml (provisional)"},
		},
		{
			name:       "unsigned exception",
			info:       plugins.PluginInfo{Unsigned: true},
			wantLines:  []string{"Signed by: (unsigned — explicit exception)"},
			wantAbsent: []string{"Cert issuer"},
		},
		{
			// The state sync reports as drift. It must read differently from
			// "no lock entry", which prints no trust line at all, and it has
			// to name the command that repairs it.
			name:       "trust unrecorded",
			info:       plugins.PluginInfo{TrustUnrecorded: true},
			wantLines:  []string{"Signed by: (trust unrecorded — run 'thv ai-plugin sync')"},
			wantAbsent: []string{"Cert issuer", "explicit exception"},
		},
		{
			name:       "no lock entry",
			info:       plugins.PluginInfo{},
			wantAbsent: []string{"Signed by", "Cert issuer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.info.Metadata = plugins.PluginMetadata{Name: "my-plugin", Version: "1.0.0"}

			// The tabwriter pads labels to the widest one in the block, which
			// differs per case; collapse runs of spaces so the assertions
			// pin the content rather than the alignment.
			out := strings.Join(strings.Fields(
				captureStdout(t, func() { printAIPluginInfoText(&tt.info) }),
			), " ")

			for _, want := range tt.wantLines {
				assert.Contains(t, out, want)
			}
			for _, absent := range tt.wantAbsent {
				assert.NotContains(t, out, absent)
			}
		})
	}
}

// TestPrintPluginInstallTrust proves install reports the trust state it
// recorded, rather than leaving the user to discover the pinned identity
// weeks later inside a signer-mismatch error.
//
//nolint:paralleltest // captures os.Stdout, which cannot be done in parallel
func TestPrintPluginInstallTrust(t *testing.T) {
	tests := []struct {
		name   string
		result *plugins.InstallResult
		want   string
	}{
		{
			name: "signed",
			result: &plugins.InstallResult{
				Plugin:     plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Provenance: &plugins.ProvenanceInfo{SignerIdentity: "/.github/workflows/release.yml"},
			},
			want: "Installed my-plugin (signed by /.github/workflows/release.yml)\n",
		},
		{
			name: "provisional",
			result: &plugins.InstallResult{
				Plugin: plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Provenance: &plugins.ProvenanceInfo{
					SignerIdentity: "/.github/workflows/release.yml",
					Provisional:    true,
				},
			},
			want: "Installed my-plugin (signed by /.github/workflows/release.yml; " +
				"verification provisional — see lock file)\n",
		},
		{
			name: "unsigned exception",
			result: &plugins.InstallResult{
				Plugin:   plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Unsigned: true,
			},
			want: "Installed my-plugin (unsigned — recorded as an explicit exception in the lock file)\n",
		},
		{
			// A user-scope install records no lock trust decision, so there
			// is nothing trust-related to report and the CLI's silent-success
			// rule applies: a bare "Installed my-plugin" would turn every
			// previously quiet install into output while saying nothing about
			// trust.
			name: "user scope, no trust state, prints nothing",
			result: &plugins.InstallResult{
				Plugin: plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
			},
			want: "",
		},
		{name: "nil result prints nothing", result: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, captureStdout(t, func() { printPluginInstallTrust(tt.result) }))
		})
	}
}
