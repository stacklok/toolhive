// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"os"
	"path/filepath"
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

// TestIntegration_SignInStampsThroughEveryStorageDecorator checks that the
// handler's user resolver finds the storage's swapper through the decorators
// newServer puts in front of it: client admission, the CIMD decorator or its
// shape guard, and the SPIFFE overlay. A row from before binding existed is
// stamped on sign-in only when the resolver has a swapper. This test fails if
// a decorator added to the production stack lacks the storageDecorator marker.
func TestIntegration_SignInStampsThroughEveryStorageDecorator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		opts   []testServerOption
		spiffe bool
	}{
		{name: "admission and shape guard"},
		{name: "admission and CIMD decorator", opts: []testServerOption{withCIMDEnabled()}},
		{name: "admission, shape guard and SPIFFE overlay", spiffe: true},
		{name: "admission, CIMD decorator and SPIFFE overlay", opts: []testServerOption{withCIMDEnabled()}, spiffe: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := startMockOIDC(t)
			opts := tt.opts
			if tt.spiffe {
				dir, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				bundlePath := filepath.Join(dir, "bundle.json")
				require.NoError(t, os.WriteFile(bundlePath, testBundleDocument(t, 0), 0o600))
				opts = append(opts, withSPIFFETrust(mustNewSPIFFETrustConfigWithSource(t, SPIFFEBundleSourceRunConfig{
					Type: SPIFFEBundleSourceTypeFile,
					File: &SPIFFEFileBundleSourceRunConfig{Path: bundlePath},
				})))
			}
			ts := setupTestServerWithOIDCProvider(t, m, opts...)

			users, ok := ts.storage.(storage.UserStorage)
			require.True(t, ok, "the server's storage must expose user storage")
			const subject = "mock-user-sub-123"
			user := &storage.User{ID: "legacy-user"}
			require.NoError(t, users.CreateUser(t.Context(), user))
			require.NoError(t, users.CreateProviderIdentity(t.Context(), &storage.ProviderIdentity{
				UserID: user.ID, ProviderID: "mockoidc", ProviderSubject: subject,
			}))

			verifier := servercrypto.GeneratePKCEVerifier()
			completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
				ClientID: testClientID, RedirectURI: testRedirectURI, State: "decorators",
				Challenge: servercrypto.ComputePKCEChallenge(verifier),
				Scope:     "openid profile", Resource: testAudience, ResponseType: "code",
			})

			row, err := users.GetProviderIdentity(t.Context(), "mockoidc", subject)
			require.NoError(t, err)
			assert.Equal(t, user.ID, row.UserID, "the unstamped row keeps its owner")
			assert.Equal(t, m.Issuer(), row.Issuer, "sign-in stamps the row, so the resolver has a swapper")
			assert.Equal(t, "sub", row.SubjectClaim)
		})
	}
}
