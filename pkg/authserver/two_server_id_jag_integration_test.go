// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// as2Env is an embedded auth server that redeems ID-JAGs minted by AS-1.
type as2Env struct {
	srv      *httptest.Server
	issuer   string
	embedded *authserverrunner.EmbeddedAuthServer
	store    storage.Storage
	// loginAudience is the resource a sign-in at AS-2 asks for.
	loginAudience string
	// client is the ID-JAG's client_id at AS-2, authenticating with private_key_jwt.
	client clientAuth
}

// twoServerIDJAG is AS-1 plus an AS-2 that allows assertions to live two
// minutes.
type twoServerIDJAG struct {
	as1 *xrtEnv
	as2 *as2Env
}

// redeem presents assertion at AS-2's token endpoint under the JWT-bearer
// grant, asking for xrtIDJAGResource.
func (e *as2Env) redeem(t *testing.T, auth clientAuth, assertion string) tokenResult {
	t.Helper()
	return postToken(t, e.srv.URL, auth, url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer},
		"assertion":  {assertion},
		"resource":   {xrtIDJAGResource},
	})
}

// startAS2 starts an AS-2 on listener that trusts the AS-1 at as1Issuer. AS-1's
// JWKS is fetched at the first redemption, so AS-1 may start later. subject is
// AS-1's sub for the grant's user, bound exactly. The server's own issuer is an
// accepted audience and is not an allowed audience; the resource is. opts are
// passed to the embedded server.
func startAS2(t *testing.T, listener net.Listener, as1Issuer, subject string, opts ...authserverrunner.EmbeddedAuthServerOption) *as2Env {
	t.Helper()
	return startAS2For(t, listener, as1Issuer, subject, []string{xrtIDJAGResource}, opts...)
}

// startAS2For is startAS2 with resources as AS-2's allowed audiences, the
// client's audiences and the subject binding's allowed resources.
func startAS2For(
	t *testing.T, listener net.Listener, as1Issuer, subject string, resources []string,
	opts ...authserverrunner.EmbeddedAuthServerOption,
) *as2Env {
	t.Helper()
	return startAS2With(t, listener, as1Issuer, subject, resources, nil, opts...)
}

// startAS2With starts an AS-2 on listener that trusts the AS-1 at as1Issuer.
// AS-1's JWKS is fetched at the first redemption, so AS-1 may start later.
// subject is AS-1's sub for the grant's user, bound exactly. The server's own
// issuer is an accepted audience and is not an allowed audience; resources are.
// resources are also the client's audiences and the binding's allowed
// resources. mutate is applied to AS-2's config last and opts are passed to the
// embedded server.
func startAS2With(
	t *testing.T, listener net.Listener, as1Issuer, subject string, resources []string,
	mutate func(cfg *authserver.RunConfig, issuer string), opts ...authserverrunner.EmbeddedAuthServerOption,
) *as2Env {
	t.Helper()
	key := newAssertionKey(t, "as2-client-key")
	jwksFile := filepath.Join(t.TempDir(), "as2-client.jwks")
	writeJWKS(t, jwksFile, key)

	// A delegate client must list token exchange; it is the jwt-bearer grant under test.
	grants := []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer}
	client := authserver.DelegateClientRunConfig{
		ClientID:                    xrtTargetClientID,
		TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		JWKSFile:                    jwksFile,
		TokenEndpointAuthSigningAlg: "ES256",
		GrantTypes:                  grants,
		Scopes:                      []string{"openid", "profile"},
		Audiences:                   resources,
		TokenExchange:               anySelfIssuedSubjectTokens(),
	}

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { assert.NoError(t, stor.Close()) })
	require.NoError(t, stor.RegisterClient(t.Context(), &fosite.DefaultClient{
		ID: xrtSourceApp, Public: true, RedirectURIs: []string{cfgRedirectURI}, ResponseTypes: []string{"code"},
		GrantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
		Scopes:     []string{"openid", "profile", "offline_access"}, Audience: resources,
	}))
	srv, issuer, embedded, _ := startEmbeddedAuthServerOn(t, listener, func(cfg *authserver.RunConfig, issuer string) {
		configuredClients(client)(cfg)
		cfg.AllowedAudiences = resources
		cfg.TrustedIssuers = []tokenexchange.TrustedIssuer{{
			Name: "as1", IssuerURL: as1Issuer, JWKSURL: as1Issuer + "/.well-known/jwks.json",
			InsecureAllowHTTP: true, AllowPrivateIPs: true,
		}}
		cfg.InboundGrants.JWTBearer = &authserver.JWTBearerInboundGrantRunConfig{
			IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
				IssuerRef: "as1", MaxAssertionAge: "2m",
				SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
					Subject: subject, AllowedResources: resources,
				}},
				AcceptedAudiences: []string{issuer},
			}},
		}
		if mutate != nil {
			mutate(cfg, issuer)
		}
	}, stor, opts...)
	return &as2Env{
		srv: srv, issuer: issuer, embedded: embedded, store: stor, loginAudience: resources[0],
		client: clientAuth{clientID: xrtTargetClientID, key: &key},
	}
}

// listenLoopback returns a listener on a free loopback port. The listener is
// closed when the test ends.
func listenLoopback(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	// Closes a listener whose server never started. After a server has
	// stopped, the second close fails harmlessly and the error is ignored.
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

// startTwoServerIDJAG is the one two-server harness in this package: AS-1
// mints ID-JAGs and AS-2 trusts AS-1 and redeems them, in one process, over
// HTTP, on memory storage. It binds both listeners first, so AS-2 can be given
// AS-1's issuer and AS-1 can be given AS-2's, then starts AS-2 and AS-1. AS-1's
// broker has one ID-JAG target: as2, with one resource.
func startTwoServerIDJAG(t *testing.T) *twoServerIDJAG {
	t.Helper()
	return startTwoServerIDJAGWith(t, nil)
}

// startTwoServerIDJAGWith is startTwoServerIDJAG with mutate applied to AS-1's
// config and opts passed to AS-1's embedded server.
func startTwoServerIDJAGWith(
	t *testing.T, mutate func(cfg *authserver.RunConfig), opts ...authserverrunner.EmbeddedAuthServerOption,
) *twoServerIDJAG {
	t.Helper()
	return startTwoServerIDJAGFor(t, []string{xrtIDJAGResource}, mutate, opts...)
}

// startTwoServerIDJAGFor is startTwoServerIDJAGWith with as2Resources as the
// resources AS-2 governs and binds. AS-1's target still names only
// xrtIDJAGResource, so its ID-JAGs carry only that resource.
func startTwoServerIDJAGFor(
	t *testing.T, as2Resources []string, mutate func(cfg *authserver.RunConfig),
	opts ...authserverrunner.EmbeddedAuthServerOption,
) *twoServerIDJAG {
	t.Helper()
	return startTwoServerIDJAGConfigured(t, as2Resources, mutate, nil, nil, opts...)
}

// startTwoServerIDJAGConfigured is startTwoServerIDJAGFor with mutateAS2
// applied to AS-2's config and as2Opts passed to AS-2's embedded server.
func startTwoServerIDJAGConfigured(
	t *testing.T, as2Resources []string, mutate func(cfg *authserver.RunConfig),
	mutateAS2 func(cfg *authserver.RunConfig, issuer string), as2Opts []authserverrunner.EmbeddedAuthServerOption,
	opts ...authserverrunner.EmbeddedAuthServerOption,
) *twoServerIDJAG {
	t.Helper()
	l1, l2 := listenLoopback(t), listenLoopback(t)
	as1Issuer := "http://" + l1.Addr().String()

	as2 := startAS2With(t, l2, as1Issuer, "user-1", as2Resources, mutateAS2, as2Opts...)

	as1 := startXRTServerOn(t, "memory", l1, func(cfg *authserver.RunConfig, _ string) {
		for _, delegate := range cfg.InboundGrants.TokenExchange.DelegateClients {
			if delegate.ClientID != cfgBrokerID {
				continue
			}
			delegate.TokenExchange.IDJAG.Targets = []authserver.DelegateClientIDJAGTarget{xrtIDJAGTarget(as2.issuer)}
		}
		if mutate != nil {
			mutate(cfg)
		}
	}, opts...)
	return &twoServerIDJAG{as1: as1, as2: as2}
}

// mintIDJAG mints an ID-JAG for AS-2 from a fresh grant for user-1.
func (h *twoServerIDJAG) mintIDJAG(t *testing.T) string {
	t.Helper()
	return h.mintIDJAGWith(t, nil)
}

// mintIDJAGWith is mintIDJAG with the request form changed by mutate.
func (h *twoServerIDJAG) mintIDJAGWith(t *testing.T, mutate func(url.Values)) string {
	t.Helper()
	grant := h.as1.idJAGGrant(t, h.as1.broker)
	res := h.as1.requestIDJAGWith(t, h.as1.broker, grant, h.as2.issuer, mutate)
	require.Equal(t, http.StatusOK, res.status, res.body)
	return res.str(t, "access_token")
}

// TestIDJAGRedemptionAcrossTwoServers proves AS-1 mints an ID-JAG that AS-2
// redeems for an access token.
func TestIDJAGRedemptionAcrossTwoServers(t *testing.T) {
	t.Parallel()
	h := startTwoServerIDJAG(t)

	res := h.as2.redeem(t, h.as2.client, h.mintIDJAG(t))
	require.Equal(t, http.StatusOK, res.status, res.body)
	access := accessTokenClaims(t, h.as2.embedded, res.str(t, "access_token"))
	assert.Equal(t, h.as1.issuer+"#user-1", access["sub"], "sub is <AS-1 iss>#<sub>")
	assert.Equal(t, h.as2.issuer, access["iss"])
	assert.Equal(t, []any{xrtIDJAGResource}, access["aud"])
	assert.Equal(t, xrtTargetClientID, access["client_id"])
	assert.NotContains(t, access, "act")
}

// login signs a user in at AS-2 through its first upstream and returns the
// login access token.
func (e *as2Env) login(t *testing.T) string {
	t.Helper()
	return realLoginAccessTokenAt(t, e.srv.URL, e.loginAudience)
}

// resolveAS2 makes AS-2's only trusted issuer a resolve issuer for ID-JAGs,
// with AS-2's first upstream the real-login IdP and idp's sub as the binding.
func resolveAS2(t *testing.T, idp *mockoidc.MockOIDC, actorIssuers ...string) func(*authserver.RunConfig, string) {
	t.Helper()
	return func(cfg *authserver.RunConfig, issuer string) {
		cfg.Upstreams = []authserver.UpstreamRunConfig{realLoginUpstream(t, idp, issuer)}
		policy := &cfg.InboundGrants.JWTBearer.IssuerPolicies[0]
		policy.SubjectBindings = nil
		policy.SubjectMode = tokenexchange.SubjectModeResolve
		policy.ActorIssuers = actorIssuers
		policy.AcceptedAssertionTypes = []tokenexchange.JWTBearerAssertionType{tokenexchange.JWTBearerAssertionTypeIDJAG}
	}
}

// requireUpstreamSubjectClaim makes the broker's ID-JAG target for AS-2 require
// the upstream_subject and sub_id claims, as a real login gives.
func requireUpstreamSubjectClaim(cfg *authserver.RunConfig) {
	for _, delegate := range cfg.InboundGrants.TokenExchange.DelegateClients {
		if delegate.ClientID == cfgBrokerID {
			delegate.TokenExchange.IDJAG.Targets[0].RequiredSubjectClaims = []string{xrtUpstreamSubject, "sub_id"}
		}
	}
}

// startResolveTwoServer is a two-server harness whose AS-2 trusts AS-1 in
// subject_mode resolve, with user-keyed storage and the real-login IdP as its
// first upstream. AS-1's broker has an actor profile, and an ID-JAG from
// mintResolveIDJAG carries upstream_subject and act. idp is the IdP both
// servers' upstream subject comes from.
func startResolveTwoServer(t *testing.T) (*twoServerIDJAG, *mockoidc.MockOIDC) {
	t.Helper()
	validator := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).Return(tokenexchange.ActorResult{
		AgentID: "spiffe://example.org/ns/agents/sa/broker", ExpiresAt: time.Now().Add(time.Hour),
	}, nil).AnyTimes()
	idp := startRealLoginUpstream(t)
	h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource},
		func(cfg *authserver.RunConfig) {
			setBrokerActorProfile(cfg)
			requireUpstreamSubjectClaim(cfg)
		},
		resolveAS2(t, idp, actorTestActIssuer),
		[]authserverrunner.EmbeddedAuthServerOption{authserverrunner.WithUserKeyedUpstreamStorage()},
		withActorTestValidator(validator))
	return h, idp
}

// mintResolveIDJAG mints an ID-JAG for AS-2 whose upstream_subject names value
// at idp, for a user AS-1 knows as "user-1".
func (h *twoServerIDJAG) mintResolveIDJAG(t *testing.T, idp *mockoidc.MockOIDC, value string) string {
	t.Helper()
	return h.mintResolveIDJAGWith(t, idp, value, nil)
}

// mintResolveIDJAGWith is mintResolveIDJAG with the ID-JAG request form changed
// by mutate.
func (h *twoServerIDJAG) mintResolveIDJAGWith(
	t *testing.T, idp *mockoidc.MockOIDC, value string, mutate func(url.Values),
) string {
	t.Helper()
	subject := h.as1.mint(t, func(_ *jwt.Claims, extra map[string]any) {
		extra["sub_id"] = map[string]any{"format": "iss_sub", "iss": idp.Issuer(), "sub": value}
		extra[claimnames.UpstreamSubjectKey] = map[string]any{"iss": idp.Issuer(), "claim": "sub", "value": value}
	})
	grant := h.as1.requestRefreshToken(t, h.as1.broker, subject, func(f url.Values) {
		f.Set("authorization_details", "["+xrtEntry(oauthproto.TokenTypeIDJAG, nil)+"]")
	})
	require.Equal(t, http.StatusOK, grant.status, grant.body)
	res := h.as1.requestIDJAGWith(t, h.as1.broker, grant.str(t, "access_token"), h.as2.issuer, func(form url.Values) {
		actorPairWith("actor-token")(form)
		if mutate != nil {
			mutate(form)
		}
	})
	require.Equal(t, http.StatusOK, res.status, res.body)
	return res.str(t, "access_token")
}

// TestIDJAGRedemptionIssuesNoRefreshToken proves a redemption that asks for
// offline_access still gets no refresh_token member, for a namespaced issuer
// and for a resolve issuer whose subject resolved to a local user, and that no
// scope is granted. A redemption has no login session (no tsid), so the
// refresh-grant guard has nothing to check and the token it issues cannot be
// refreshed.
func TestIDJAGRedemptionIssuesNoRefreshToken(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// start returns AS-2, an ID-JAG for it and the sub the token must carry.
		start func(t *testing.T) (*as2Env, string, string)
	}{
		{"namespaced issuer", func(t *testing.T) (*as2Env, string, string) {
			t.Helper()
			h := startTwoServerIDJAG(t)
			return h.as2, h.mintIDJAG(t), h.as1.issuer + "#user-1"
		}},
		{"resolve issuer", func(t *testing.T) (*as2Env, string, string) {
			t.Helper()
			h, idp := startResolveTwoServer(t)
			login := accessTokenClaims(t, h.as2.embedded, h.as2.login(t))
			user, ok := login["sub"].(string)
			require.True(t, ok)
			require.NotEqual(t, realLoginUpstreamSubject, user, "the local user is not the upstream subject")
			return h.as2, h.mintResolveIDJAG(t, idp, realLoginUpstreamSubject), user
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			as2, assertion, wantSub := tt.start(t)

			res := postToken(t, as2.srv.URL, as2.client, url.Values{
				"grant_type": {oauthproto.GrantTypeJWTBearer},
				"assertion":  {assertion},
				"resource":   {xrtIDJAGResource},
				"scope":      {"openid offline_access"},
			})
			require.Equal(t, http.StatusOK, res.status, res.body)
			assert.NotContains(t, res.body, "refresh_token")
			// Redemption grants no scope. fosite encodes "granted nothing" as an
			// empty scope member, which RFC 6749 section 3.3 requires because it
			// differs from the requested scope.
			assert.Equal(t, "", res.body["scope"])
			assert.Equal(t, "bearer", res.body["token_type"])
			access := accessTokenClaims(t, as2.embedded, res.str(t, "access_token"))
			assert.Equal(t, wantSub, access["sub"])
			assert.Empty(t, access["scp"])
			assert.NotContains(t, access, "scope")
		})
	}
}

// TestResolvedIDJAGRedemptionIsSingleUseAndRetries proves, over HTTP and real
// replay storage, that a resolve issuer's ID-JAG redeems once: presenting it
// again is refused as a used assertion and leaves the first token stored. A
// fresh ID-JAG for the same user then redeems to an equivalent token. That is
// the retry a client like Mecatl makes. Its ID-JAG request carries an extra
// mecatl_operation parameter, which AS-1 neither refuses nor copies into the
// ID-JAG as a claim.
func TestResolvedIDJAGRedemptionIsSingleUseAndRetries(t *testing.T) {
	t.Parallel()
	h, idp := startResolveTwoServer(t)
	user, ok := accessTokenClaims(t, h.as2.embedded, h.as2.login(t))["sub"].(string)
	require.True(t, ok)
	assertion := h.mintResolveIDJAG(t, idp, realLoginUpstreamSubject)

	first := h.as2.redeem(t, h.as2.client, assertion)
	require.Equal(t, http.StatusOK, first.status, first.body)
	firstToken := first.str(t, "access_token")
	firstClaims := accessTokenClaims(t, h.as2.embedded, firstToken)
	assert.Equal(t, user, firstClaims["sub"])

	// Mecatl sees invalid_grant, whose description starts with the
	// invalid_assertion reason code and says the assertion was already used.
	// It retries with a new ID-JAG on invalid_grant, and only a new ID-JAG can
	// succeed. The delegated agent flow test pins the exact description.
	replay := h.as2.redeem(t, h.as2.client, assertion)
	assert.Equal(t, http.StatusBadRequest, replay.status, replay.body)
	assert.Equal(t, "invalid_grant", replay.body["error"])
	assert.NotContains(t, replay.body, "access_token")
	// A refused replay revokes nothing: the first token's session is still stored.
	_, err := h.as2.store.GetAccessTokenSession(t.Context(), firstToken[strings.LastIndex(firstToken, ".")+1:], nil)
	require.NoError(t, err)

	retryAssertion := h.mintResolveIDJAGWith(t, idp, realLoginUpstreamSubject, func(form url.Values) {
		form.Set("mecatl_operation", "tools/call")
	})
	_, idjagClaims := decodeIDJAG(t, retryAssertion)
	assert.NotContains(t, idjagClaims, "mecatl_operation")
	retry := h.as2.redeem(t, h.as2.client, retryAssertion)
	require.Equal(t, http.StatusOK, retry.status, retry.body)
	retryToken := retry.str(t, "access_token")
	require.NotEqual(t, firstToken, retryToken)
	retryClaims := accessTokenClaims(t, h.as2.embedded, retryToken)
	for _, perToken := range []string{"jti", "iat", "nbf", "exp"} {
		delete(firstClaims, perToken)
		delete(retryClaims, perToken)
	}
	assert.Equal(t, firstClaims, retryClaims)
}

// TestIDJAGRedemptionCarriesActorAcrossTwoServers proves the act claim AS-1
// puts in an ID-JAG for a client with an actor profile survives redemption at
// AS-2 into AS-2's signed access token.
func TestIDJAGRedemptionCarriesActorAcrossTwoServers(t *testing.T) {
	t.Parallel()
	const agentID = "spiffe://example.org/ns/agents/sa/broker"
	validator := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).Return(tokenexchange.ActorResult{
		AgentID: agentID, ExpiresAt: time.Now().Add(time.Hour),
	}, nil).Times(1)
	h := startTwoServerIDJAGWith(t, setBrokerActorProfile, withActorTestValidator(validator))

	res := h.as2.redeem(t, h.as2.client, h.mintIDJAGWith(t, actorPairWith("actor-token")))
	require.Equal(t, http.StatusOK, res.status, res.body)
	access := accessTokenClaims(t, h.as2.embedded, res.str(t, "access_token"))
	assert.Equal(t, h.as1.issuer+"#user-1", access["sub"], "sub is <AS-1 iss>#<sub>")
	assert.Equal(t, map[string]any{"iss": actorTestActIssuer, "sub": agentID}, access["act"])
}

// TestIDJAGRedemptionUsesClientAccessTokenLifespan proves a delegate client's
// access_token_lifespan in AS-2's RunConfig reaches ID-JAG redemption: the
// token's exp and the response's expires_in both follow it. AS-1's default
// ID-JAG lifetime is 60 seconds, longer than the client's 20.
func TestIDJAGRedemptionUsesClientAccessTokenLifespan(t *testing.T) {
	t.Parallel()
	const lifespan = 20 * time.Second
	h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, nil,
		func(cfg *authserver.RunConfig, _ string) {
			clients := cfg.InboundGrants.TokenExchange.DelegateClients
			for i := range clients {
				if clients[i].ClientID == xrtTargetClientID {
					clients[i].AccessTokenLifespan = lifespan.String()
				}
			}
		}, nil)

	res := h.as2.redeem(t, h.as2.client, h.mintIDJAG(t))
	require.Equal(t, http.StatusOK, res.status, res.body)
	assert.InDelta(t, lifespan.Seconds(), res.body["expires_in"], 3)
	access := accessTokenClaims(t, h.as2.embedded, res.str(t, "access_token"))
	exp, ok := access["exp"].(float64)
	require.True(t, ok, "exp must be a number")
	iat, ok := access["iat"].(float64)
	require.True(t, ok, "iat must be a number")
	// exp - iat takes the runner's speed out of it: both are signed in whole
	// seconds from the same session, so they differ by the lifespan, give or
	// take the second boundary between them.
	assert.InDelta(t, lifespan.Seconds(), exp-iat, 1)
}

// TestIDJAGRedemptionRefusesResourceTheClaimDoesNotCover proves AS-2 refuses an
// ID-JAG for a resource its resource claim does not name, and issues no token.
// AS-2 governs both resources, the client may have both and the subject is
// bound to both, so only the claim can refuse. The refusal leaves the
// assertion unconsumed: the same ID-JAG then redeems for the resource it names.
func TestIDJAGRedemptionRefusesResourceTheClaimDoesNotCover(t *testing.T) {
	t.Parallel()
	const otherResource = "https://gw.example.net/other-mcp"
	h := startTwoServerIDJAGFor(t, []string{xrtIDJAGResource, otherResource}, nil)
	assertion := h.mintIDJAG(t)
	_, claims := decodeIDJAG(t, assertion)
	require.Equal(t, xrtIDJAGResource, claims["resource"], "the ID-JAG names only the first resource")

	res := postToken(t, h.as2.srv.URL, h.as2.client, url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer},
		"assertion":  {assertion},
		"resource":   {otherResource},
	})
	assert.Equal(t, http.StatusBadRequest, res.status, res.body)
	assert.Equal(t, "invalid_target", res.body["error"])
	assert.NotContains(t, res.body["error_description"], "other-mcp", "the description must not echo the resource")
	assert.NotContains(t, res.body, "access_token")

	res = h.as2.redeem(t, h.as2.client, assertion)
	require.Equal(t, http.StatusOK, res.status, res.body)
	access := accessTokenClaims(t, h.as2.embedded, res.str(t, "access_token"))
	assert.Equal(t, []any{xrtIDJAGResource}, access["aud"])
}

// TestIDJAGRedemptionFromResolveIssuerChecksActor proves a resolve issuer
// accepts the act.iss AS-1's real issuance puts in the ID-JAG (not AS-1's own
// issuer) when actor_issuers lists it, so the redemption goes on to subject
// resolution; with it unlisted, the actor check refuses first. AS-1 here adds
// no upstream_subject claim, so a listed issuer's redemption ends at
// resolution as an invalid assertion.
func TestIDJAGRedemptionFromResolveIssuerChecksActor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		actorIssuers []string
		wantError    string
		wantReason   string
	}{
		{
			"actor issuer listed", []string{actorTestActIssuer}, "invalid_grant",
			"invalid_assertion: The ID-JAG assertion has a missing or malformed upstream_subject claim.",
		},
		{"actor issuer not listed", []string{"https://other-agents.example.com"}, "access_denied", "actor_denied"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			validator := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).Return(tokenexchange.ActorResult{
				AgentID: "spiffe://example.org/ns/agents/sa/broker", ExpiresAt: time.Now().Add(time.Hour),
			}, nil).Times(1)
			idp := startRealLoginUpstream(t)
			h := startTwoServerIDJAGConfigured(t, []string{xrtIDJAGResource}, setBrokerActorProfile,
				func(cfg *authserver.RunConfig, issuer string) {
					cfg.Upstreams = []authserver.UpstreamRunConfig{realLoginUpstream(t, idp, issuer)}
					policy := &cfg.InboundGrants.JWTBearer.IssuerPolicies[0]
					policy.SubjectBindings = nil
					policy.SubjectMode = tokenexchange.SubjectModeResolve
					policy.ActorIssuers = tc.actorIssuers
					policy.AcceptedAssertionTypes = []tokenexchange.JWTBearerAssertionType{tokenexchange.JWTBearerAssertionTypeIDJAG}
				}, nil, withActorTestValidator(validator))

			idJAG := h.mintIDJAGWith(t, actorPairWith("actor-token"))
			parsed, err := jwt.ParseSigned(idJAG, []jose.SignatureAlgorithm{jose.RS256, jose.ES256})
			require.NoError(t, err)
			var claims map[string]any
			require.NoError(t, parsed.UnsafeClaimsWithoutVerification(&claims))
			act, ok := claims["act"].(map[string]any)
			require.True(t, ok, "the ID-JAG must carry act")
			assert.Equal(t, actorTestActIssuer, act["iss"])

			res := h.as2.redeem(t, h.as2.client, idJAG)
			assert.Equal(t, http.StatusBadRequest, res.status, res.body)
			assert.Equal(t, tc.wantError, res.body["error"])
			assert.Equal(t, tc.wantReason, res.body["error_description"])
			assert.NotContains(t, res.body, "access_token")
		})
	}
}
