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

// testCLIPublicKeyB64 stands in for the base64 DER SPKI a key-pinned lock
// entry records; the CLI renders it verbatim and parses nothing.
const testCLIPublicKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExlVDpbnOEv2fH3gS8n7UCHS9Gs0wKxIPR5EAcl8F1jSxlxAV/pll0NsSiuAK95Ws4Fpkn+5QkdVKNXy7LHgb2A=="

// TestAIPluginPushSigningFlags pins the signed-by-default publish surface:
// all three signing flags must exist, and neither signing method nor the
// opt-out may be preset — a defaulted --no-sign would publish unsigned
// artifacts silently. --key's help must name the --public-key step its
// consumers need, since the signing key is recoverable from neither the
// artifact nor its bundle, so a publisher who is not told to distribute it
// ships an artifact nobody can install project-scoped.
func TestAIPluginPushSigningFlags(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"key", "identity-token", "no-sign"} {
		flag := aiPluginPushCmd.Flags().Lookup(name)
		require.NotNil(t, flag, "thv ai-plugin push must expose --%s", name)
	}
	assert.Equal(t, "false", aiPluginPushCmd.Flags().Lookup("no-sign").DefValue,
		"pushing unsigned must always be an explicit choice")
	assert.Empty(t, aiPluginPushCmd.Flags().Lookup("key").DefValue)
	assert.Empty(t, aiPluginPushCmd.Flags().Lookup("identity-token").DefValue)
	assert.Contains(t, aiPluginPushCmd.Flags().Lookup("key").Usage, "--public-key",
		"--key must tell publishers consumers need --public-key on first install")
	assert.Contains(t, aiPluginPushCmd.Flags().Lookup("identity-token").Usage, "Mutually exclusive with --key",
		"the two signing methods are mutually exclusive and the help must say so")
}

// TestAIPluginInstallKeyFlag pins the consuming half of key signing: without
// --public-key on install there is no way to supply the trust anchor a
// key-signed artifact needs, since the key is recoverable from neither the
// artifact nor its bundle.
func TestAIPluginInstallKeyFlag(t *testing.T) {
	t.Parallel()

	flag := aiPluginInstallCmd.Flags().Lookup("public-key")
	require.NotNil(t, flag, "thv ai-plugin install must expose --public-key")
	assert.Empty(t, flag.DefValue, "there is no default trust anchor to assume")
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
			// A key-pinned entry has no signer identity and no cert issuer, so
			// the identity rendering would print empty values and read exactly
			// like an untracked install.
			name: "key pinned",
			info: plugins.PluginInfo{Provenance: &plugins.ProvenanceInfo{
				PublicKey: testCLIPublicKeyB64,
			}},
			wantLines: []string{
				"Signed by: (cosign key pair)",
				"Public key: " + testCLIPublicKeyB64,
			},
			wantAbsent: []string{"Cert issuer", "provisional", "unsigned"},
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
			name: "key pinned",
			result: &plugins.InstallResult{
				Plugin:     plugins.InstalledPlugin{Metadata: plugins.PluginMetadata{Name: "my-plugin"}},
				Provenance: &plugins.ProvenanceInfo{PublicKey: testCLIPublicKeyB64},
			},
			want: "Installed my-plugin (signed by a cosign key pair; " +
				"the pinned public key is in the lock file)\n",
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
