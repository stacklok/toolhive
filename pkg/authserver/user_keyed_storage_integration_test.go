// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oauth2-proxy/mockoidc"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	serveridentity "github.com/stacklok/toolhive/pkg/authserver/identity"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	userKeyedSecondClientID = "second-client"
	// userKeyedRowSessionPrefix is the session ID prefix the wrapper uses for a
	// user's row. A foreign row can only be planted below the wrapper, so the
	// tests address the base store with the wrapper's own key shape.
	userKeyedRowSessionPrefix = "user:"
)

// userKeyedBackends runs a test over the in-memory and the miniredis-backed store.
var userKeyedBackends = []struct {
	name string
	opts []testServerOption
}{
	{"memory", nil},
	{"redis", []testServerOption{withRedisBackedStorage()}},
}

func userKeyedOpts(backend []testServerOption, opts ...testServerOption) []testServerOption {
	return append(append([]testServerOption{withUserKeyedUpstreamStorage()}, backend...), opts...)
}

func userKeyedSecondClient() *fosite.DefaultClient {
	return &fosite.DefaultClient{
		ID:            userKeyedSecondClientID,
		RedirectURIs:  []string{testRedirectURI},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		Scopes:        registration.DefaultScopes,
		Audience:      []string{testAudience},
		Public:        true,
	}
}

// deleteSpyStorage counts upstream-token deletes that reach it. Placed below the
// user-keyed wrapper it sees what the wrapper forwards; placed above it, what
// the guard and the refresher ask for. It decorates real storage because every
// call must pass through to it, which a gomock mock does not do.
type deleteSpyStorage struct {
	storage.Storage
	deletes atomic.Int32
}

func (s *deleteSpyStorage) Unwrap() storage.Storage { return s.Storage }

func (s *deleteSpyStorage) ConsumeAssertionJWT(
	ctx context.Context, purpose, issuer, jti string, exp time.Time,
) error {
	return s.Storage.(storage.AssertionJWTConsumer).ConsumeAssertionJWT(ctx, purpose, issuer, jti, exp)
}

func (s *deleteSpyStorage) DeleteUpstreamTokens(ctx context.Context, sessionID string) error {
	s.deletes.Add(1)
	return s.Storage.DeleteUpstreamTokens(ctx, sessionID)
}

func (s *deleteSpyStorage) DeleteUpstreamTokensForProvider(ctx context.Context, sessionID, provider string) error {
	s.deletes.Add(1)
	return s.Storage.DeleteUpstreamTokensForProvider(ctx, sessionID, provider)
}

func serverOf(t *testing.T, ts *testServer) *server {
	t.Helper()
	srv, ok := ts.authServer.(*server)
	require.True(t, ok)
	return srv
}

func canonicalUserCtx(userID string) context.Context {
	return serveridentity.WithCanonicalUser(context.Background(), userID)
}

// claimsFromTokens decodes the access token of a token response.
func claimsFromTokens(t *testing.T, ts *testServer, tokens map[string]any) map[string]any {
	t.Helper()
	access, ok := tokens["access_token"].(string)
	require.True(t, ok)
	return decodeTestAccessToken(t, ts, access)
}

func stringClaim(t *testing.T, claims map[string]any, name string) string {
	t.Helper()
	v, ok := claims[name].(string)
	require.True(t, ok, "claim %q", name)
	require.NotEmpty(t, v)
	return v
}

func queueChainUsers(m1, m2 *mockoidc.MockOIDC, logins int) {
	for range logins {
		m1.QueueUser(&mockoidc.MockUser{Subject: "user-from-provider-1", Email: "user1@provider1.example.com"})
		m2.QueueUser(&mockoidc.MockUser{Subject: "user-from-provider-2", Email: "user2@provider2.example.com"})
	}
}

func startTwoMockOIDC(t *testing.T) (*mockoidc.MockOIDC, *mockoidc.MockOIDC) {
	t.Helper()
	m1, err := mockoidc.Run()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m1.Shutdown()) })
	m2, err := mockoidc.Run()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m2.Shutdown()) })
	return m1, m2
}

// fullChainLogin walks both legs for clientID and returns the access-token claims.
func fullChainLogin(t *testing.T, ts *testServer, clientID string) map[string]any {
	t.Helper()
	verifier := servercrypto.GeneratePKCEVerifier()
	code := runChainFlowAs(t, ts.Server.URL, clientID, servercrypto.ComputePKCEChallenge(verifier), "state-for-"+clientID)
	return claimsFromTokens(t, ts, exchangeCodeForTokensAs(t, ts.Server.URL, clientID, code, verifier, testAudience))
}

func TestUserKeyedStorage_ServerWiring(t *testing.T) {
	t.Parallel()
	for _, backend := range userKeyedBackends {
		t.Run(backend.name+"/field on", func(t *testing.T) {
			t.Parallel()
			ts := setupTestServer(t, userKeyedOpts(backend.opts)...)
			srv := serverOf(t, ts)

			wrapper, ok := srv.storage.(*storage.UserKeyedUpstreamStorage)
			require.True(t, ok, "wrapper must be outermost, got %T", srv.storage)
			refresher := srv.upstreamRefresher
			require.NotNil(t, refresher)
			assert.Same(t, wrapper, refresher.storage)
			assert.Same(t, wrapper, srv.IDPTokenStorage())

			requireSingleClientAdmission(t, srv.IDPTokenStorage())
			_, ok = storage.ClientStoreBelowCIMD(srv.storage).(*storage.ClientAdmissionStorage)
			assert.True(t, ok, "client lookup must still reach the admission layer")
		})
		t.Run(backend.name+"/field off", func(t *testing.T) {
			t.Parallel()
			ts := setupTestServer(t, backend.opts...)
			var s any = serverOf(t, ts).storage
			for {
				_, isWrapper := s.(*storage.UserKeyedUpstreamStorage)
				require.False(t, isWrapper, "no user-keying layer expected with the field off")
				unwrapper, ok := s.(interface{ Unwrap() storage.Storage })
				if !ok {
					break
				}
				s = unwrapper.Unwrap()
			}
		})
	}
}

func TestUserKeyedStorage_JWTBearerReplayRefused(t *testing.T) {
	t.Parallel()
	ts, signAssertion := setupJWTBearerGrantTestServer(t, userKeyedOpts(nil)...)
	now := time.Now()
	params := url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer},
		"assertion":  {signAssertion("external-subject", now, now.Add(2*time.Minute), "user-keyed-replay")},
		"resource":   {testAudience},
	}

	first := makeTokenRequest(t, ts.Server.URL, params)
	defer first.Body.Close()
	require.Equal(t, http.StatusOK, first.StatusCode, parseTokenResponse(t, first))

	second := makeTokenRequest(t, ts.Server.URL, params)
	defer second.Body.Close()
	body := parseTokenResponse(t, second)
	assert.Equal(t, http.StatusBadRequest, second.StatusCode)
	assert.Equal(t, "invalid_grant", body["error"])
}

func TestUserKeyedStorage_RememberedConsentAndDCR(t *testing.T) {
	t.Parallel()
	t.Run("remembered consent", func(t *testing.T) {
		t.Parallel()
		m := startMockOIDC(t)
		const origin = "https://auth.example.com"
		ts := setupTestServerWithMockOIDC(t, m, userKeyedOpts(nil, withAuthorizationEndpointBaseURL(origin))...)
		params := authorizationParams{
			ClientID: testClientID, RedirectURI: testRedirectURI, State: "remembered-state",
			Challenge: strings.Repeat("A", 43), Scope: "openid", Resource: testAudience, ResponseType: "code",
		}
		_, _, cookies := completeAuthorizationFlowWithConsent(t, ts.Server.URL, params, origin, true)

		req, err := http.NewRequest(http.MethodGet, ts.Server.URL+"/oauth/authorize?"+url.Values{
			"client_id": {testClientID}, "redirect_uri": {testRedirectURI}, "state": {"again-state"},
			"code_challenge": {params.Challenge}, "code_challenge_method": {"S256"},
			"response_type": {"code"}, "scope": {"openid"}, "resource": {testAudience},
		}.Encode(), nil)
		require.NoError(t, err)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := noRedirectClient().Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusSeeOther, resp.StatusCode, "a remembered grant skips the consent page")
	})
	t.Run("DCR registration", func(t *testing.T) {
		t.Parallel()
		m := startMockOIDC(t)
		ts := setupTestServerWithMockOIDC(t, m,
			userKeyedOpts(nil, withAllowConfidentialClientRegistration())...)
		clientID, secret := registerConfidentialClient(t, ts.Server.URL, oauthproto.TokenEndpointAuthMethodClientSecretBasic)
		assert.NotEmpty(t, clientID)
		assert.NotEmpty(t, secret)
	})
}

func TestUserKeyedStorage_DeviceFlow(t *testing.T) {
	t.Parallel()
	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m,
		userKeyedOpts(nil, withExtraClient(deviceFlowClient()), withDeviceFlowEnabled(),
			withDeviceCodeInterval())...).testServer

	resp, body := postDeviceAuthorization(t, ts.Server.URL, url.Values{
		"client_id": {testDeviceFlowClientID}, "scope": {"openid"},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %v", body)
	deviceCode, _ := body["device_code"].(string)
	userCode, _ := body["user_code"].(string)
	require.NotEmpty(t, deviceCode)
	require.NotEmpty(t, userCode)

	completeDeviceVerification(t, ts.Server.URL, userCode)
	time.Sleep(20 * time.Millisecond)

	okResp := makeTokenRequest(t, ts.Server.URL, url.Values{
		"grant_type":  {oauthproto.GrantTypeDeviceCode},
		"device_code": {deviceCode},
		"client_id":   {testDeviceFlowClientID},
	})
	defer okResp.Body.Close()
	tokens := parseTokenResponse(t, okResp)
	require.Equal(t, http.StatusOK, okResp.StatusCode, "body: %v", tokens)

	sub := stringClaim(t, claimsFromTokens(t, ts, tokens), "sub")
	rows, err := ts.storage.GetAllUpstreamTokens(canonicalUserCtx(sub), "")
	require.NoError(t, err)
	require.Len(t, rows, 1, "device login must store the user's row")
	for _, row := range rows {
		assert.Equal(t, sub, row.UserID)
	}
}

func TestUserKeyedStorage_SignInUserReadableWithoutCanonicalUser(t *testing.T) {
	t.Parallel()
	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m, withUserKeyedUpstreamStorage()).testServer
	verifier := servercrypto.GeneratePKCEVerifier()
	code, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
		ClientID: testClientID, RedirectURI: testRedirectURI, State: "state-signin",
		Challenge: servercrypto.ComputePKCEChallenge(verifier), Scope: "openid", ResponseType: "code",
	})
	sub := stringClaim(t, claimsFromTokens(t, ts, exchangeCodeForTokens(t, ts.Server.URL, code, verifier, testAudience)), "sub")

	// The subject resolver reads users and provider identities with a context
	// that carries no canonical user; those methods must pass through.
	user, err := serverOf(t, ts).storage.GetUser(context.Background(), sub)
	require.NoError(t, err)
	assert.Equal(t, sub, user.ID)
}

func TestUserKeyedStorage_TwoClientsShareConnections(t *testing.T) {
	t.Parallel()
	for _, backend := range userKeyedBackends {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			m1, m2 := startTwoMockOIDC(t)
			queueChainUsers(m1, m2, 2)
			ts := setupTestServerWithTwoUpstreams(t, m1, m2,
				userKeyedOpts(backend.opts, withExtraClient(userKeyedSecondClient()))...)

			first := fullChainLogin(t, ts, testClientID)

			// Client B logs in as the same user: every leg but the identity
			// provider's is skipped, so leg 1's callback answers 303 to the client.
			verifier := servercrypto.GeneratePKCEVerifier()
			resp := runFirstLegAs(t, ts.Server.URL, userKeyedSecondClientID,
				servercrypto.ComputePKCEChallenge(verifier), "state-client-b")
			defer resp.Body.Close()
			require.Equal(t, http.StatusSeeOther, resp.StatusCode, "connected providers must be skipped")
			location, err := resp.Location()
			require.NoError(t, err)
			second := claimsFromTokens(t, ts, exchangeCodeForTokensAs(t, ts.Server.URL, userKeyedSecondClientID,
				location.Query().Get("code"), verifier, testAudience))

			sub := stringClaim(t, first, "sub")
			assert.Equal(t, sub, stringClaim(t, second, "sub"))
			tsidA := stringClaim(t, first, claimnames.TokenSessionIDKey)
			tsidB := stringClaim(t, second, claimnames.TokenSessionIDKey)
			require.NotEqual(t, tsidA, tsidB)

			base := storage.Unwrap(serverOf(t, ts).storage)
			for _, provider := range []string{"provider-1", "provider-2"} {
				row, err := ts.storage.GetUpstreamTokens(canonicalUserCtx(sub), tsidA, provider)
				require.NoError(t, err)
				assert.Equal(t, sub, row.UserID)
				for _, tsid := range []string{tsidA, tsidB} {
					_, err := base.GetUpstreamTokens(context.Background(), tsid, provider)
					require.ErrorIs(t, err, storage.ErrNotFound, "no session-keyed row may be written (tsid %s)", tsid)
				}
			}
		})
	}
}

func TestUserKeyedStorage_ForeignRowIsReplacedWithoutDeletes(t *testing.T) {
	t.Parallel()
	for _, backend := range userKeyedBackends {
		t.Run(backend.name, func(t *testing.T) {
			t.Parallel()
			m1, m2 := startTwoMockOIDC(t)
			queueChainUsers(m1, m2, 2)
			spy := &deleteSpyStorage{}
			ts := setupTestServerWithTwoUpstreams(t, m1, m2, userKeyedOpts(backend.opts,
				withExtraClient(userKeyedSecondClient()),
				withStorageDecorator(func(s storage.Storage) storage.Storage {
					spy.Storage = s
					return spy
				}))...)

			sub := stringClaim(t, fullChainLogin(t, ts, testClientID), "sub")

			// The wrapper refuses to write a row whose owner is not the context
			// user, so the foreign row goes in below it, under the user's key.
			require.NoError(t, spy.StoreUpstreamTokens(context.Background(),
				userKeyedRowSessionPrefix+url.QueryEscape(sub), "provider-2", &storage.UpstreamTokens{
					ProviderID: "provider-2", AccessToken: "intruder-token", UserID: "someone-else",
					ExpiresAt: time.Now().Add(time.Hour), UpstreamSubject: "x", ClientID: testClientID,
				}))

			// The owner check skips the foreign row, so the chain walk runs the
			// second leg again; runChainFlowAs fails unless leg 1 redirects to it.
			second := fullChainLogin(t, ts, userKeyedSecondClientID)
			assert.Equal(t, sub, stringClaim(t, second, "sub"))

			row, err := ts.storage.GetUpstreamTokens(canonicalUserCtx(sub), "", "provider-2")
			require.NoError(t, err)
			assert.Equal(t, sub, row.UserID)
			assert.NotEqual(t, "intruder-token", row.AccessToken)
			assert.Zero(t, spy.deletes.Load(), "no upstream row may be deleted")
		})
	}
}

func syntheticChainUpstream(name string, m *mockoidc.MockOIDC) UpstreamConfig {
	cfg := chainUpstreamOAuth2(name, m)
	cfg.OAuth2Config.UserInfo = nil
	return cfg
}

func TestUserKeyedStorage_SyntheticSubjects(t *testing.T) {
	t.Parallel()
	t.Run("synthetic first upstream", func(t *testing.T) {
		t.Parallel()
		m := startMockOIDC(t)
		ts := setupTestServer(t, userKeyedOpts(nil, withUpstream(newSyntheticOAuth2Provider(t, m)))...)

		login := func() (string, map[string]any) {
			verifier := servercrypto.GeneratePKCEVerifier()
			code, _ := completeAuthorizationFlow(t, ts.Server.URL, authorizationParams{
				ClientID: testClientID, RedirectURI: testRedirectURI, State: "synthetic-state",
				Challenge: servercrypto.ComputePKCEChallenge(verifier),
				Scope:     "openid profile offline_access", ResponseType: "code",
			})
			tokens := exchangeCodeForTokens(t, ts.Server.URL, code, verifier, testAudience)
			return tokens["refresh_token"].(string), tokens
		}
		refreshToken, tokens1 := login()
		sub1 := stringClaim(t, claimsFromTokens(t, ts, tokens1), "sub")
		require.True(t, upstream.IsSynthesizedSubject(sub1))

		refreshed := refreshForAccessToken(t, ts.Server.URL, refreshToken)
		assert.NotEmpty(t, refreshed)

		_, tokens2 := login()
		sub2 := stringClaim(t, claimsFromTokens(t, ts, tokens2), "sub")
		require.NotEqual(t, sub1, sub2, "a synthetic subject changes on every login")

		row2, err := ts.storage.GetUpstreamTokens(canonicalUserCtx(sub2), "", "default")
		require.NoError(t, err)
		assert.Equal(t, sub2, row2.UserID)
		row1, err := ts.storage.GetUpstreamTokens(canonicalUserCtx(sub1), "", "default")
		require.NoError(t, err)
		assert.Equal(t, sub1, row1.UserID, "login 1's row is read only by its own user")

		assert.NotEqual(t, row1.AccessToken, row2.AccessToken)

		_, err = ts.storage.GetUpstreamTokens(canonicalUserCtx("tk-never-logged-in"), "", "default")
		require.ErrorIs(t, err, storage.ErrNotFound)
	})
	t.Run("synthetic second leg", func(t *testing.T) {
		t.Parallel()
		m1, m2 := startTwoMockOIDC(t)
		queueChainUsers(m1, m2, 1)
		ts := setupTestServerWithUpstreamChain(t, []UpstreamConfig{
			chainUpstreamOIDC("idp", m1), syntheticChainUpstream("connector", m2),
		}, userKeyedOpts(nil)...)

		claims := fullChainLogin(t, ts, testClientID)
		sub := stringClaim(t, claims, "sub")
		rows, err := ts.storage.GetAllUpstreamTokens(canonicalUserCtx(sub), "")
		require.NoError(t, err)
		assert.Contains(t, rows, "idp")
		assert.Contains(t, rows, "connector")
	})
}

// TestUserKeyedStorage_DisconnectThroughIDPTokenStorage shows the disconnect
// an embedder offers: deleting one provider's row through IDPTokenStorage()
// with the user on the context. The next login of the user, from any client,
// treats that provider as not connected and runs its leg again.
func TestUserKeyedStorage_DisconnectThroughIDPTokenStorage(t *testing.T) {
	t.Parallel()
	m1, m2 := startTwoMockOIDC(t)
	queueChainUsers(m1, m2, 2)
	ts := setupTestServerWithTwoUpstreams(t, m1, m2,
		userKeyedOpts(nil, withExtraClient(userKeyedSecondClient()))...)

	sub := stringClaim(t, fullChainLogin(t, ts, testClientID), "sub")

	idpTokens := ts.authServer.IDPTokenStorage()
	require.NoError(t, idpTokens.DeleteUpstreamTokensForProvider(canonicalUserCtx(sub), "", "provider-2"))

	// runChainFlowAs fails unless leg 1 redirects to the second leg, so
	// the disconnected provider is no longer skipped.
	second := fullChainLogin(t, ts, userKeyedSecondClientID)
	assert.Equal(t, sub, stringClaim(t, second, "sub"))
	row, err := idpTokens.GetUpstreamTokens(canonicalUserCtx(sub), "", "provider-2")
	require.NoError(t, err)
	assert.Equal(t, sub, row.UserID)
}
