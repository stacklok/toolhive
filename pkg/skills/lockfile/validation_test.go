// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package lockfile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	validSHA256Hex     = hexDigest(sha256HexLength, 0)
	validSHA256Digest  = "sha256:" + validSHA256Hex
	validGitSHA1       = hexDigest(sha1HexLength, 0)
	validContentDigest = ContentDigestPrefix + validSHA256Hex
)

// testPublicKeyB64 is a real P-256 public key in the base64 DER SPKI
// form a key-pinned lock entry stores. It must genuinely parse: validation
// rejects a value that merely decodes as base64.
const testPublicKeyB64 = "MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAExlVDpbnOEv2fH3gS8n7UCHS9Gs0wKxIPR5EAcl8F1jSxlxAV/pll0NsSiuAK95Ws4Fpkn+5QkdVKNXy7LHgb2A=="

func TestValidateDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		digest  string
		wantErr string
	}{
		{name: "valid OCI digest", digest: validSHA256Digest},
		{name: "valid git SHA-1 commit", digest: validGitSHA1},
		{name: "valid git SHA-256 commit", digest: validSHA256Hex},
		{name: "empty", digest: "", wantErr: "expected"},
		{name: "abbreviated git hash rejected", digest: "0123456", wantErr: "expected"},
		{name: "oci digest wrong length", digest: "sha256:abc", wantErr: "OCI digest"},
		{name: "oci digest bad hex", digest: "sha256:" + strings.Repeat("z", 64), wantErr: "OCI digest"},
		{name: "git hash bad hex", digest: strings.Repeat("z", 40), wantErr: "git commit hash"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateDigest(tt.digest)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateContentDigest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		digest  string
		wantErr string
	}{
		{name: "valid", digest: validContentDigest},
		{name: "missing prefix", digest: validSHA256Hex, wantErr: "must start with"},
		{name: "wrong length", digest: ContentDigestPrefix + "abc", wantErr: "expected 64 hex"},
		{name: "bad hex", digest: ContentDigestPrefix + strings.Repeat("z", 64), wantErr: "invalid hex"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateContentDigest(tt.digest)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateResolvedReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		ref     string
		wantErr string
	}{
		{name: "valid OCI reference with tag", ref: "ghcr.io/org/skill:1.0.0"},
		{name: "valid OCI reference with digest", ref: "ghcr.io/org/skill@sha256:" + validSHA256Hex},
		{name: "valid git reference", ref: "git://github.com/org/repo@main#skills/my-skill"},
		{name: "too long", ref: "ghcr.io/org/" + strings.Repeat("a", maxReferenceLength), wantErr: "exceeds"},
		{name: "leading whitespace", ref: " ghcr.io/org/skill:1", wantErr: "whitespace"},
		{name: "embedded newline", ref: "ghcr.io/org/\nskill:1", wantErr: "non-graphic"},
		{name: "embedded ANSI escape", ref: "ghcr.io/org/\x1b[31mskill:1", wantErr: "non-graphic"},
		{name: "malformed git reference", ref: "git://", wantErr: "invalid git reference"},
		{name: "not a reference at all", ref: "http://169.254.169.254/latest/meta-data", wantErr: "not a valid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateResolvedReference(tt.ref)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestValidateLockfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		lf      Lockfile
		wantErr string
	}{
		{
			name: "valid single entry",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "my-skill", Source: "my-skill", Digest: validSHA256Digest},
			}},
		},
		{
			name:    "unsupported version",
			lf:      Lockfile{Version: 99},
			wantErr: "unsupported lock file version",
		},
		{
			name: "duplicate entry names",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "dup", Source: "a", Digest: validSHA256Digest},
				{Name: "dup", Source: "b", Digest: validSHA256Digest},
			}},
			wantErr: "duplicate entry",
		},
		{
			name: "requiredBy references unknown parent",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "dep", Source: "dep", Digest: validSHA256Digest, RequiredBy: []string{"ghost"}},
			}},
			wantErr: "unknown parent",
		},
		{
			name: "requiredBy references itself",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "dep", Source: "dep", Digest: validSHA256Digest, RequiredBy: []string{"dep"}},
			}},
			wantErr: "references itself",
		},
		{
			name: "missing source",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "my-skill", Digest: validSHA256Digest},
			}},
			wantErr: "source is required",
		},
		{
			name: "missing digest",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "my-skill", Source: "my-skill"},
			}},
			wantErr: "digest is required",
		},
		{
			name: "invalid skill name",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "Not_Valid", Source: "x", Digest: validSHA256Digest},
			}},
			wantErr: "entry name",
		},
		{
			// A mutual requiredBy ring of non-explicit entries would pass the
			// per-edge checks yet be impossible to ever cascade-remove.
			name: "requiredBy cycle",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "ring-a", Source: "a", Digest: validSHA256Digest, RequiredBy: []string{"ring-b"}},
				{Name: "ring-b", Source: "b", Digest: validSHA256Digest, RequiredBy: []string{"ring-a"}},
			}},
			wantErr: "requiredBy cycle",
		},
		{
			name: "valid provenance",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity: "dev@example.com",
					CertIssuer:     "https://accounts.example.com",
				}},
			}},
		},
		{
			name: "provenance and unsigned are mutually exclusive",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "conflicted", Source: "s", Digest: validSHA256Digest, Unsigned: true, Provenance: &Provenance{
					SignerIdentity: "dev@example.com",
					CertIssuer:     "https://accounts.example.com",
				}},
			}},
			wantErr: "mutually exclusive",
		},
		{
			name: "publicKey-pinned provenance accepted without an identity",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					PublicKey: testPublicKeyB64,
				}},
			}},
		},
		{
			name: "publicKey and a certificate identity are mutually exclusive",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "both", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity: "dev@example.com",
					CertIssuer:     "https://accounts.example.com",
					PublicKey:      testPublicKeyB64,
				}},
			}},
			wantErr: "mutually exclusive",
		},
		{
			name: "certificate-derived fields rejected on a publicKey-pinned entry",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					PublicKey:     testPublicKeyB64,
					RepositoryRef: "refs/tags/v1",
				}},
			}},
			wantErr: "read from a certificate",
		},
		{
			name: "publicKey must decode to a DER SPKI key, not merely to bytes",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					// Valid base64 of ASCII text: well-encoded, but not a key.
					PublicKey: "bm90LWEta2V5LWp1c3QtdGV4dA==",
				}},
			}},
			wantErr: "not a DER SPKI public key",
		},
		{
			// No resolvedReference, so the entry is classified by the only signal
			// left: a bare commit hash means a git entry, whose signature lives
			// on the commit and is verified against a certificate, never a key.
			name: "publicKey rejected on a git entry classified by its digest",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: strings.Repeat("a", 40), Provenance: &Provenance{
					PublicKey: testPublicKeyB64,
				}},
			}},
			wantErr: "only valid for an OCI artifact",
		},
		{
			// The same rejection reached through the resolved reference, with a
			// digest that agrees with it: the anchor check fires on its own
			// account here, not as a side effect of the two fields disagreeing.
			name: "publicKey rejected on a git entry classified by its resolved reference",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validGitSHA1,
					ResolvedReference: "git://github.com/org/repo@main#skills/keyed",
					Provenance:        &Provenance{PublicKey: testPublicKeyB64},
				},
			}},
			wantErr: "only valid for an OCI artifact",
		},
		{
			name: "publicKey must be base64",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					// Graphic and whitespace-free, so the syntactic checks pass
					// and the base64 decode is genuinely what rejects it. PEM
					// armor would be caught earlier, by its embedded spaces.
					PublicKey: "-----BEGINPUBLICKEY-----",
				}},
			}},
			wantErr: "not valid base64",
		},
		{
			name: "provenance missing signer identity",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					CertIssuer: "https://accounts.example.com",
				}},
			}},
			wantErr: "signerIdentity is required",
		},
		{
			name: "provenance missing cert issuer",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity: "dev@example.com",
				}},
			}},
			wantErr: "certIssuer is required",
		},
		{
			name: "provenance with control characters rejected",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity: "dev@example.com\x1b[31m",
					CertIssuer:     "https://accounts.example.com",
				}},
			}},
			wantErr: "non-graphic",
		},
		{
			name: "provenance field too long rejected",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity: strings.Repeat("a", maxReferenceLength+1),
					CertIssuer:     "https://accounts.example.com",
				}},
			}},
			wantErr: "exceeds",
		},
		{
			name: "provenance with ref and runner environment is valid",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity:    "/.github/workflows/release.yml",
					CertIssuer:        "https://token.actions.githubusercontent.com",
					RepositoryRef:     "refs/heads/main",
					RunnerEnvironment: "github-hosted",
				}},
			}},
		},
		{
			name: "provenance repositoryRef with control characters rejected",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity: "dev@example.com",
					CertIssuer:     "https://accounts.example.com",
					RepositoryRef:  "refs/heads/ma\x1b[31min",
				}},
			}},
			wantErr: "non-graphic",
		},
		{
			name: "provenance runnerEnvironment too long rejected",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "signed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					SignerIdentity:    "dev@example.com",
					CertIssuer:        "https://accounts.example.com",
					RunnerEnvironment: strings.Repeat("a", maxReferenceLength+1),
				}},
			}},
			wantErr: "exceeds",
		},
		{
			name: "unsigned exception alone is valid",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "unsigned", Source: "s", Digest: validSHA256Digest, Unsigned: true},
			}},
		},
		{
			// The reported bypass: restore dispatches on resolvedReference, so
			// classifying the entry by its digest alone let a git entry carry an
			// OCI-only key anchor and pushed the malformed trust decision out to
			// fetch time.
			name: "publicKey rejected on a git entry whose digest is written in OCI form",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest,
					ResolvedReference: "git://github.com/org/repo@main#skills/keyed",
					Provenance:        &Provenance{PublicKey: testPublicKeyB64},
				},
			}},
			wantErr: "resolvedReference is a git reference",
		},
		{
			// The disagreement is malformed on its own account, key anchor or
			// not: buildPinnedReference would splice the OCI digest into a git
			// reference and produce "git://github.com/org/repo@sha256:...".
			name: "git resolvedReference with an OCI digest rejected without any provenance",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "mixed", Source: "s", Digest: validSHA256Digest,
					ResolvedReference: "git://github.com/org/repo@main#skills/mixed"},
			}},
			wantErr: "a git entry pins a full commit hash",
		},
		{
			name: "OCI resolvedReference with a git commit hash digest rejected",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "mixed", Source: "s", Digest: validGitSHA1,
					ResolvedReference: "ghcr.io/org/mixed:1.0.0"},
			}},
			wantErr: "an OCI entry pins",
		},
		{
			// Plugins are a separate graph validated by the same per-entry
			// checks; the "plugins:" prefix is what attributes the failure to
			// the right half of the file.
			name: "plugin git resolvedReference with an OCI digest rejected",
			lf: Lockfile{Version: CurrentVersion, Plugins: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest,
					ResolvedReference: "git://github.com/org/repo@main#plugins/keyed",
					Provenance:        &Provenance{PublicKey: testPublicKeyB64},
				},
			}},
			wantErr: "plugins: entry \"keyed\": digest is an OCI manifest digest",
		},
		{
			name: "git resolvedReference with a matching commit hash is valid",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "gitted", Source: "s", Digest: validGitSHA1,
					ResolvedReference: "git://github.com/org/repo@main#skills/gitted"},
			}},
		},
		{
			name: "OCI resolvedReference with a matching manifest digest is valid",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "ocied", Source: "s", Digest: validSHA256Digest,
					ResolvedReference: "ghcr.io/org/ocied:1.0.0",
					Provenance:        &Provenance{PublicKey: testPublicKeyB64},
				},
			}},
		},
		{
			// Key material needs more room than an identifier: an RSA-4096
			// SPKI encodes to 736 characters, so the reference bound would
			// reject a legitimate anchor rather than the garbage it guards
			// against. Reaching the SPKI parse at this length proves the
			// narrower bound is not the one being applied.
			name: "publicKey is bounded above the reference limit",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					PublicKey: strings.Repeat("A", maxReferenceLength+4),
				}},
			}},
			wantErr: "not a DER SPKI public key",
		},
		{
			// The length must be rejected before the value is base64-decoded,
			// so the allocation is bounded by a checked number rather than by
			// whatever the lock file happens to contain.
			name: "publicKey beyond its own bound rejected before decoding",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "keyed", Source: "s", Digest: validSHA256Digest, Provenance: &Provenance{
					PublicKey: strings.Repeat("A", MaxEncodedPublicKeyLength+4),
				}},
			}},
			wantErr: "exceeds",
		},
		{
			name: "requiredBy diamond is not a cycle",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "shared-dep", Source: "d", Digest: validSHA256Digest, RequiredBy: []string{"parent-a", "parent-b"}},
				{Name: "parent-a", Source: "a", Digest: validSHA256Digest, RequiredBy: []string{"root"}},
				{Name: "parent-b", Source: "b", Digest: validSHA256Digest, RequiredBy: []string{"root"}},
				{Name: "root", Source: "r", Digest: validSHA256Digest, Explicit: true},
			}},
		},
		{
			name: "valid plugin entry",
			lf: Lockfile{Version: CurrentVersion, Plugins: []Entry{
				{Name: "my-plugin", Source: "my-plugin", Digest: validSHA256Digest},
			}},
		},
		{
			name: "skill and plugin may share a name",
			lf: Lockfile{Version: CurrentVersion,
				Skills:  []Entry{{Name: "shared", Source: "s", Digest: validSHA256Digest}},
				Plugins: []Entry{{Name: "shared", Source: "p", Digest: validSHA256Digest}},
			},
		},
		{
			name: "duplicate plugin entry names",
			lf: Lockfile{Version: CurrentVersion, Plugins: []Entry{
				{Name: "dup", Source: "a", Digest: validSHA256Digest},
				{Name: "dup", Source: "b", Digest: validSHA256Digest},
			}},
			wantErr: "duplicate entry",
		},
		{
			name: "plugin requiredBy references unknown parent",
			lf: Lockfile{Version: CurrentVersion, Plugins: []Entry{
				{Name: "dep", Source: "dep", Digest: validSHA256Digest, RequiredBy: []string{"ghost"}},
			}},
			wantErr: "unknown parent",
		},
		{
			name: "plugin requiredBy cycle",
			lf: Lockfile{Version: CurrentVersion, Plugins: []Entry{
				{Name: "ring-a", Source: "a", Digest: validSHA256Digest, RequiredBy: []string{"ring-b"}},
				{Name: "ring-b", Source: "b", Digest: validSHA256Digest, RequiredBy: []string{"ring-a"}},
			}},
			wantErr: "requiredBy cycle",
		},
		{
			name: "skill requiredBy cannot name a plugin",
			lf: Lockfile{Version: CurrentVersion,
				Skills:  []Entry{{Name: "dep", Source: "d", Digest: validSHA256Digest, RequiredBy: []string{"plug"}}},
				Plugins: []Entry{{Name: "plug", Source: "p", Digest: validSHA256Digest}},
			},
			wantErr: "unknown parent",
		},
		{
			name: "plugin requiredBy cannot name a skill",
			lf: Lockfile{Version: CurrentVersion,
				Skills:  []Entry{{Name: "sk", Source: "s", Digest: validSHA256Digest}},
				Plugins: []Entry{{Name: "dep", Source: "d", Digest: validSHA256Digest, RequiredBy: []string{"sk"}}},
			},
			wantErr: "unknown parent",
		},
		{
			name: "plugin missing source",
			lf: Lockfile{Version: CurrentVersion, Plugins: []Entry{
				{Name: "my-plugin", Digest: validSHA256Digest},
			}},
			wantErr: "source is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lf := tt.lf
			err := validateLockfile(&lf)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}
