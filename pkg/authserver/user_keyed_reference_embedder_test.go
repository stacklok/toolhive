// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// sessionRecordingStorage is the storage decorator an embedder places below the
// user-keyed layer. It records the session ID of every upstream-token call that
// reaches it.
type sessionRecordingStorage struct {
	storage.Storage
	mu  sync.Mutex
	ids map[string]struct{}
}

func (s *sessionRecordingStorage) Unwrap() storage.Storage { return s.Storage }

func (s *sessionRecordingStorage) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	return s.Storage.(storage.AssertionJWTConsumer).ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

func (s *sessionRecordingStorage) record(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = make(map[string]struct{})
	}
	s.ids[sessionID] = struct{}{}
}

func (s *sessionRecordingStorage) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.ids))
	for id := range s.ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (s *sessionRecordingStorage) StoreUpstreamTokens(
	ctx context.Context, sessionID, provider string, tokens *storage.UpstreamTokens,
) error {
	s.record(sessionID)
	return s.Storage.StoreUpstreamTokens(ctx, sessionID, provider, tokens)
}

func (s *sessionRecordingStorage) CompareAndSwapUpstreamTokens(
	ctx context.Context, sessionID, provider, expectedRefreshToken string, tokens *storage.UpstreamTokens,
) error {
	s.record(sessionID)
	return s.Storage.CompareAndSwapUpstreamTokens(ctx, sessionID, provider, expectedRefreshToken, tokens)
}

func (s *sessionRecordingStorage) ResolveUpstreamTokenRowID(
	ctx context.Context, sessionID, provider string,
) (storage.UpstreamTokenRowID, error) {
	s.record(sessionID)
	return s.Storage.ResolveUpstreamTokenRowID(ctx, sessionID, provider)
}

func (s *sessionRecordingStorage) GetUpstreamTokens(
	ctx context.Context, sessionID, provider string,
) (*storage.UpstreamTokens, error) {
	s.record(sessionID)
	return s.Storage.GetUpstreamTokens(ctx, sessionID, provider)
}

func (s *sessionRecordingStorage) GetAllUpstreamTokens(
	ctx context.Context, sessionID string,
) (map[string]*storage.UpstreamTokens, error) {
	s.record(sessionID)
	return s.Storage.GetAllUpstreamTokens(ctx, sessionID)
}

func (s *sessionRecordingStorage) DeleteUpstreamTokens(ctx context.Context, sessionID string) error {
	s.record(sessionID)
	return s.Storage.DeleteUpstreamTokens(ctx, sessionID)
}

func (s *sessionRecordingStorage) DeleteUpstreamTokensForProvider(ctx context.Context, sessionID, provider string) error {
	s.record(sessionID)
	return s.Storage.DeleteUpstreamTokensForProvider(ctx, sessionID, provider)
}

// forwardingReader is an embedder's TokenReader wrapper that passes the
// user-keyed capability of the reader it wraps through to the validator.
type forwardingReader struct{ upstreamtoken.TokenReader }

func (r forwardingReader) UserKeyed() bool {
	keyed, ok := r.TokenReader.(upstreamtoken.UserKeyedReader)
	return ok && keyed.UserKeyed()
}

// hidingReader is the same wrapper written without the capability.
type hidingReader struct{ upstreamtoken.TokenReader }

type referenceEmbedder struct {
	ts       *testServer
	recorder *sessionRecordingStorage
}

func newReferenceEmbedder(t *testing.T, backend []testServerOption) *referenceEmbedder {
	t.Helper()
	m1, m2 := startTwoMockOIDC(t)
	queueChainUsers(m1, m2, 2)
	recorder := &sessionRecordingStorage{}
	ts := setupTestServerWithTwoUpstreams(t, m1, m2, userKeyedOpts(backend,
		withExtraClient(userKeyedSecondClient()),
		withStorageDecorator(func(s storage.Storage) storage.Storage {
			recorder.Storage = s
			return recorder
		}))...)
	return &referenceEmbedder{ts: ts, recorder: recorder}
}

// reader is the embedder's reader over the server's IDPTokenStorage().
func (e *referenceEmbedder) reader() upstreamtoken.TokenReader {
	return upstreamtoken.NewInProcessService(e.ts.authServer.IDPTokenStorage(), e.ts.authServer.UpstreamTokenRefresher())
}

// validator builds the embedder's validator. The key provider is always set;
// bind adds the binding to the embedded issuer.
func (e *referenceEmbedder) validator(reader upstreamtoken.TokenReader, bind bool, audience string) (*auth.TokenValidator, error) {
	opts := []auth.TokenValidatorOption{
		auth.WithKeyProvider(&testKeyProvider{key: e.ts.PrivateKey}),
		auth.WithUpstreamTokenReader(reader),
	}
	if bind {
		opts = append(opts, auth.WithEmbeddedAuthServerIssuer(testIssuer))
	}
	return auth.NewTokenValidator(context.Background(),
		auth.TokenValidatorConfig{Issuer: testIssuer, Audience: audience}, opts...)
}

// call sends a bearer request through the validator middleware and returns the
// response with the identity the downstream handler saw (nil if it never ran).
func call(t *testing.T, v *auth.TokenValidator, accessToken string) (*httptest.ResponseRecorder, *auth.Identity) {
	t.Helper()
	var seen *auth.Identity
	handler := v.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = auth.IdentityFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec, seen
}

func requireProviders(t *testing.T, v *auth.TokenValidator, accessToken string, providers ...string) map[string]string {
	t.Helper()
	rec, identity := call(t, v, accessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, identity)
	got := make([]string, 0, len(identity.UpstreamTokens))
	for provider := range identity.UpstreamTokens {
		got = append(got, provider)
	}
	assert.ElementsMatch(t, providers, got)
	return identity.UpstreamTokens
}

func accessTokenOf(t *testing.T, tokens map[string]any) string {
	t.Helper()
	access, ok := tokens["access_token"].(string)
	require.True(t, ok)
	return access
}

// loginFirstClient walks both legs and returns the token response.
func (e *referenceEmbedder) loginFirstClient(t *testing.T) map[string]any {
	t.Helper()
	verifier := servercrypto.GeneratePKCEVerifier()
	code := runChainFlowAs(t, e.ts.Server.URL, testClientID, servercrypto.ComputePKCEChallenge(verifier), "state-first")
	return exchangeCodeForTokensAs(t, e.ts.Server.URL, testClientID, code, verifier, testAudience)
}

// loginSecondClient logs the same user in through the second client, asking for
// a refresh token. Every leg but the identity provider's is skipped, so leg 1
// answers 303 to the client.
func (e *referenceEmbedder) loginSecondClient(t *testing.T) map[string]any {
	t.Helper()
	verifier := servercrypto.GeneratePKCEVerifier()
	resp := runFirstLegWithScopeAs(t, e.ts.Server.URL, userKeyedSecondClientID,
		servercrypto.ComputePKCEChallenge(verifier), "state-second", "openid profile offline_access")
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode, "connected providers must be skipped")
	location, err := resp.Location()
	require.NoError(t, err)
	return exchangeCodeForTokensAs(t, e.ts.Server.URL, userKeyedSecondClientID,
		location.Query().Get("code"), verifier, testAudience)
}

// TestUserKeyedStorage_ReferenceEmbedder wires the server the way an embedder
// does: its own storage decorator below the user-keyed layer, its own
// TokenReader wrapper over IDPTokenStorage() that forwards the user-keyed
// capability, a validator with the three requirements (the embedded issuer
// binding, the in-process key provider, an audience), and an HTTP handler
// behind the validator middleware. It follows the adoption checklist in
// docs/arch/11-auth-server-storage.md: two clients share one user's upstream
// connections, a refresh grant keeps working, a disconnect through
// IDPTokenStorage() shows up on the next request, deleting the user removes the
// rows, and the decorator only ever sees user-keyed session IDs. The miswired
// cases show the symptom of each wiring mistake. The checklist items about a
// second user-keyed layer (5), the per-server key prefix (6), migrating rows
// written before the option was on (7), zero expiry fields (8), and the refresh
// guard ordering (10) are not exercised, beyond the refresh grant passing the
// built-in guard.
func TestUserKeyedStorage_ReferenceEmbedder(t *testing.T) {
	t.Parallel()
	for _, backend := range userKeyedBackends {
		t.Run(backend.name+"/happy path", func(t *testing.T) {
			t.Parallel()
			e := newReferenceEmbedder(t, backend.opts)
			v, err := e.validator(forwardingReader{e.reader()}, true, testAudience)
			require.NoError(t, err)
			providers := []string{"provider-1", "provider-2"}

			first := e.loginFirstClient(t)
			firstLoad := requireProviders(t, v, accessTokenOf(t, first), providers...)
			sub := stringClaim(t, claimsFromTokens(t, e.ts, first), "sub")

			second := e.loginSecondClient(t)
			secondLoad := requireProviders(t, v, accessTokenOf(t, second), providers...)
			// Client B's login re-ran the identity provider's leg, so provider-1's
			// row was rewritten; provider-2's leg was skipped and its row is shared.
			assert.Equal(t, firstLoad["provider-2"], secondLoad["provider-2"])

			refreshToken, ok := second["refresh_token"].(string)
			require.True(t, ok, "the second client's token response must carry a refresh token")
			resp := makeTokenRequest(t, e.ts.Server.URL, url.Values{
				"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {userKeyedSecondClientID},
			})
			defer resp.Body.Close()
			refreshed := parseTokenResponse(t, resp)
			require.Equal(t, http.StatusOK, resp.StatusCode, refreshed)
			requireProviders(t, v, accessTokenOf(t, refreshed), providers...)

			// Login and refresh write through compare-and-swap only when the
			// upstream returns no refresh token or its access token has expired,
			// which the mock IdP does not do, so exercise those calls directly.
			idpTokens := e.ts.authServer.IDPTokenStorage()
			row, err := idpTokens.GetUpstreamTokens(canonicalUserCtx(sub), "", "provider-1")
			require.NoError(t, err)
			require.NoError(t, idpTokens.CompareAndSwapUpstreamTokens(
				canonicalUserCtx(sub), "", "provider-1", row.RefreshToken, row))
			_, err = idpTokens.ResolveUpstreamTokenRowID(canonicalUserCtx(sub), "", "provider-1")
			require.NoError(t, err)

			// A row removed for one provider is not an error: the validator loads
			// what is left and the request still succeeds.
			require.NoError(t, idpTokens.DeleteUpstreamTokensForProvider(canonicalUserCtx(sub), "", "provider-2"))
			requireProviders(t, v, accessTokenOf(t, refreshed), "provider-1")

			users, ok := idpTokens.(storage.UserStorage)
			require.True(t, ok, "IDPTokenStorage() must expose DeleteUser")
			require.NoError(t, users.DeleteUser(context.Background(), sub))
			// provider-2 was disconnected above, so only provider-1 shows the deletion.
			_, err = idpTokens.GetUpstreamTokens(canonicalUserCtx(sub), "", "provider-1")
			require.ErrorIs(t, err, storage.ErrNotFound)

			seen := e.recorder.seen()
			require.NotEmpty(t, seen)
			for _, id := range seen {
				assert.True(t, strings.HasPrefix(id, userKeyedRowSessionPrefix),
					"the decorator below the layer saw a session ID that is not user-keyed")
			}
		})

		t.Run(backend.name+"/miswired", func(t *testing.T) {
			t.Parallel()
			e := newReferenceEmbedder(t, backend.opts)
			accessToken := accessTokenOf(t, e.loginFirstClient(t))

			tests := []struct {
				name       string
				reader     upstreamtoken.TokenReader
				bind       bool
				audience   string
				wantErr    string
				wantStatus int
				wantAuth   string
			}{
				{
					name:       "positive control: forwarding reader, bound validator",
					reader:     forwardingReader{e.reader()},
					bind:       true,
					audience:   testAudience,
					wantStatus: http.StatusOK,
				},
				{
					// Not user-keyed, so construction succeeds; the tsid-keyed
					// lookup finds no row and the user looks disconnected.
					name:       "reader over the storage below the layer",
					reader:     upstreamtoken.NewInProcessService(e.recorder, nil),
					bind:       true,
					audience:   testAudience,
					wantStatus: http.StatusUnauthorized,
					wantAuth:   "re-authentication required",
				},
				{
					name:       "reader wrapper hides the capability, validator unbound",
					reader:     hidingReader{e.reader()},
					bind:       false,
					audience:   testAudience,
					wantStatus: http.StatusServiceUnavailable,
				},
				{
					name:     "forwarding reader, validator without an audience",
					reader:   forwardingReader{e.reader()},
					bind:     true,
					audience: "",
					wantErr:  "an audience",
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					v, err := e.validator(tt.reader, tt.bind, tt.audience)
					if tt.wantErr != "" {
						require.ErrorContains(t, err, tt.wantErr)
						return
					}
					require.NoError(t, err)
					rec, identity := call(t, v, accessToken)
					assert.Equal(t, tt.wantStatus, rec.Code)
					assert.Contains(t, rec.Header().Get("WWW-Authenticate"), tt.wantAuth)
					if tt.wantStatus == http.StatusOK {
						assert.NotNil(t, identity)
						return
					}
					assert.Nil(t, identity, "the handler must not run")
				})
			}

			t.Run("disconnect with an Identity but no canonical user", func(t *testing.T) {
				t.Parallel()
				sub := stringClaim(t, claimsFromTokens(t, e.ts, map[string]any{"access_token": accessToken}), "sub")
				idpTokens := e.ts.authServer.IDPTokenStorage()
				// The layer reads only the canonical-user key, never the Identity.
				ctx := auth.WithIdentity(context.Background(), &auth.Identity{
					PrincipalInfo: auth.PrincipalInfo{Subject: sub, PlatformUserID: sub},
				})
				err := idpTokens.DeleteUpstreamTokensForProvider(ctx, "", "provider-2")
				require.ErrorIs(t, err, storage.ErrNoCanonicalUser)
				_, err = idpTokens.GetUpstreamTokens(canonicalUserCtx(sub), "", "provider-2")
				require.NoError(t, err, "the row must still be there")
			})
		})
	}
}
