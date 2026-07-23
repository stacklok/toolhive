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
			name: "requiredBy diamond is not a cycle",
			lf: Lockfile{Version: CurrentVersion, Skills: []Entry{
				{Name: "shared-dep", Source: "d", Digest: validSHA256Digest, RequiredBy: []string{"parent-a", "parent-b"}},
				{Name: "parent-a", Source: "a", Digest: validSHA256Digest, RequiredBy: []string{"root"}},
				{Name: "parent-b", Source: "b", Digest: validSHA256Digest, RequiredBy: []string{"root"}},
				{Name: "root", Source: "r", Digest: validSHA256Digest, Explicit: true},
			}},
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
