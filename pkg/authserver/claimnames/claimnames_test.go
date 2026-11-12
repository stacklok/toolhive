// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package claimnames

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQualifySubject(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://idp.example.com#alice", QualifySubject("https://idp.example.com", "alice"))
}

func TestIsQualifiedSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		sub  string
		want bool
	}{
		{
			name: "qualified subject",
			sub:  "https://idp.example.com#alice",
			want: true,
		},
		{
			name: "native UUID",
			sub:  "3f2b8c1e-5a7d-4c9e-9b1a-0d2e4f6a8b0c",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tc.want, IsQualifiedSubject(tc.sub))
		})
	}
}

func TestClaimKeyWireSpellings(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "tsid", TokenSessionIDKey)
	assert.Equal(t, "https://toolhive.dev/no_upstream_session", NoUpstreamSessionKey)
	assert.Equal(t, "external_issuer", ExternalIssuerKey)
	assert.Equal(t, "sub_id", SubjectIdentifierKey)
	assert.Equal(t, "https://toolhive.dev/upstream_subject", UpstreamSubjectKey)
}
