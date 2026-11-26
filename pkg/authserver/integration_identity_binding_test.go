// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// TestIntegration_SignInStampsConfiguredIssuerAndClaim signs in through the
// real constructor and handler and checks that the stored identity link carries
// the issuer and subject claim the upstream is configured with.
func TestIntegration_SignInStampsConfiguredIssuerAndClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		subjectClaim string
		redis        bool
		wantClaim    string
		wantSubject  string
	}{
		{name: "default claim on memory storage", wantClaim: "sub", wantSubject: "mock-user-sub-123"},
		{
			name: "configured claim on memory storage", subjectClaim: "email",
			wantClaim: "email", wantSubject: "testuser@example.com",
		},
		{
			name: "configured claim on Redis storage", subjectClaim: "email", redis: true,
			wantClaim: "email", wantSubject: "testuser@example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := startMockOIDC(t)
			var opts []testServerOption
			if tt.subjectClaim != "" {
				opts = append(opts, withOIDCSubjectClaim(tt.subjectClaim))
			}
			if tt.redis {
				opts = append(opts, withRedisBackedStorage())
			}
			ts := setupTestServerWithOIDCProvider(t, m, opts...)

			verifier := servercrypto.GeneratePKCEVerifier()
			completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
				ClientID: testClientID, RedirectURI: testRedirectURI, State: "binding-" + tt.name,
				Challenge: servercrypto.ComputePKCEChallenge(verifier),
				Scope:     "openid profile", Resource: testAudience, ResponseType: "code",
			})

			users, ok := ts.storage.(storage.UserStorage)
			require.True(t, ok, "the server's storage must expose user storage")
			row, err := users.GetProviderIdentity(t.Context(), "mockoidc", tt.wantSubject)
			require.NoError(t, err)
			assert.Equal(t, m.Issuer(), row.Issuer)
			assert.Equal(t, tt.wantClaim, row.SubjectClaim)
		})
	}
}
