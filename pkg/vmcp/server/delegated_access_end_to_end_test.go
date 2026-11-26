// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/oauth2-proxy/mockoidc"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	actormocks "github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

const (
	delegatedActIssuer  = "https://agents.example"
	delegatedCompany    = "company-idp"
	delegatedGitHub     = "github"
	delegatedTool       = "list_repos"
	delegatedGitToken   = "gh-token-alice"
	delegatedGitRefresh = "gh-refresh-alice"
	delegatedAlice      = "company-alice"
	delegatedBob        = "company-bob"
)

// The subtests share one fixture, one session and one store, and run in order.
//
//nolint:paralleltest,tparallel // sequential subtests share one fixture
func TestDelegatedAccessResolvedUserCallsGitHub(t *testing.T) {
	skipWhileSubjectResolutionIsClosed(t)
	t.Parallel()
	f := startDelegatedFixture(t, "memory")
	aliceGrant := f.openGrant(t, f.login(t, f.as1Issuer, chainLoginAud, delegatedAlice, f.idp.Issuer()))
	aliceJAG := f.mint(t, 0, aliceGrant, "agent-1")
	login := f.login(t, f.as2Issuer, chainResource, delegatedAlice, f.idp.Issuer(), f.githubURL)
	user, ok := issuedTokenClaims(t, f.as2, login)["sub"].(string)
	require.True(t, ok)
	require.NotEmpty(t, user)
	store := f.as2.IDPTokenStorage()
	userCtx := identity.WithCanonicalUser(t.Context(), user)
	row, err := store.GetUpstreamTokens(userCtx, "", delegatedGitHub)
	require.NoError(t, err)
	require.Equal(t, delegatedGitToken, row.AccessToken)

	alice := f.redeem(t, 0, aliceJAG).accessToken(t)
	assertDelegatedToken(t, f.as2, alice, user, chainAS2Client, "agent-1")
	require.Equal(t, user, row.UserID)

	gateway := f.gateway(t)
	backend, company := gateway.github, gateway.company
	handler := gateway.handler(t, nil)
	owner := credentialRPC{handler: handler, token: alice, version: mcpparser.MCPVersionLegacy}
	t.Run("Alice calls GitHub with her stored credential", func(t *testing.T) {
		owner.warm(t, delegatedTool, company.tool)
		before := len(backend.headers())
		owner.requireSuccess(t, delegatedTool, nil)
		assertCredentialHeaders(t, backend.headers()[before:], delegatedGitToken)
		assertCredentialHeaders(t, backend.headers(), delegatedGitToken)
	})
	require.NotEmpty(t, owner.session)

	t.Run("Bob has no local AS-2 user", func(t *testing.T) {
		requireNoGitHubCall(t, backend)
		bob := f.login(t, f.as1Issuer, chainLoginAud, delegatedBob, f.idp.Issuer())
		bobJAG := f.mint(t, 0, f.openGrant(t, bob), "agent-1")
		f.redeem(t, 0, bobJAG).refused(t, "access_denied", "subject_unknown")
	})
	for _, tc := range []struct {
		name, agent string
		client      int
	}{
		{"different client on Alice's session", "agent-1", 1},
		{"different actor on Alice's session", "agent-2", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoGitHubCall(t, backend)
			grant := aliceGrant
			if tc.client != 0 {
				// Each AS-1 broker has one target per issuer; use a fresh
				// login for the exchange grant owned by the other broker.
				login := f.login(t, f.as1Issuer, chainLoginAud, delegatedAlice, f.idp.Issuer())
				grant = f.brokers[tc.client].openGrant(t, f.as1Issuer, login)
			}
			caller := owner
			caller.token = f.redeem(t, tc.client, f.mint(t, tc.client, grant, tc.agent)).accessToken(t)
			assertDelegatedToken(t, f.as2, caller.token, user, f.clients[tc.client].id, tc.agent)
			res := caller.call(t, delegatedTool, nil)
			assert.Equal(t, http.StatusNotFound, res.Code, res.Body.String())
			assert.Equal(t, "Session terminated\n", res.Body.String())
		})
	}
	t.Run("marked access token cannot be exchanged again", func(t *testing.T) {
		requireNoGitHubCall(t, backend)
		res := f.clients[0].post(t, f.as2Issuer, url.Values{
			"grant_type": {oauthproto.GrantTypeTokenExchange}, "resource": {chainResource},
			"subject_token": {alice}, "subject_token_type": {oauthproto.TokenTypeAccessToken},
		})
		res.refused(t, "invalid_request", "")
		assert.Contains(t, res.body["error_description"], "The subject token is a delegated-access token and cannot be exchanged.")
	})
	resign := func(t *testing.T, mutate func(map[string]any)) string {
		t.Helper()
		claims := issuedTokenClaims(t, f.as1, aliceJAG)
		claims["jti"] = rand.Text()
		mutate(claims)
		key, err := f.as1.KeyProvider().SigningKey(t.Context())
		require.NoError(t, err)
		signer := joseSigner(t, jose.SignatureAlgorithm(key.Algorithm), key.Key, key.KeyID, oauthproto.JWTTypeIDJAG)
		return signJWT(t, signer, claims)
	}
	t.Run("re-signed ID-JAG with unchanged claims redeems", func(t *testing.T) {
		assertDelegatedToken(t, f.as2, f.redeem(t, 0, resign(t, func(map[string]any) {})).accessToken(t), user, chainAS2Client, "agent-1")
	})
	for _, tc := range []struct {
		name, description string
		mutate            func(map[string]any)
	}{
		{"two-element audience", "invalid_assertion: The ID-JAG assertion aud must name exactly one audience.", func(c map[string]any) {
			c["aud"] = []string{f.as2Issuer, "https://other.example"}
		}},
		{"confirmation claim", "invalid_assertion: The ID-JAG assertion must not carry a 'cnf' claim.", func(c map[string]any) {
			c["cnf"] = map[string]string{"jkt": "test-thumbprint"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requireNoGitHubCall(t, backend)
			f.redeem(t, 0, resign(t, tc.mutate)).refused(t, "invalid_grant", tc.description)
		})
	}
	t.Run("resolve issuer requires a signed resource claim", func(t *testing.T) {
		requireNoGitHubCall(t, backend)
		assertion := resign(t, func(c map[string]any) { delete(c, "resource") })
		res := f.clients[0].post(t, f.as2Issuer, url.Values{
			"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {assertion}, "resource": {chainResource},
		})
		res.refused(t, "invalid_grant", "invalid_assertion: The ID-JAG assertion must carry a 'resource' claim.")
	})
	t.Run("Alice disconnects GitHub", func(t *testing.T) {
		requireNoGitHubCall(t, backend)
		require.NoError(t, store.DeleteUpstreamTokensForProvider(userCtx, "", delegatedGitHub))
		t.Cleanup(func() { require.NoError(t, store.StoreUpstreamTokens(userCtx, "", delegatedGitHub, row)) })

		res := owner.call(t, delegatedTool, nil)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		result := toolCredentialResult(t, res)
		assert.True(t, result.IsError)
		assert.Equal(t, []mcp.Content{&mcp.TextContent{Text: `provider "github" requires upstream authorization`}}, result.Content)

		// Use a cold capability cache. A new session on the warmed server can
		// reuse its per-identity discovery snapshot until the cache expires.
		fresh := credentialRPC{
			handler: gateway.handler(t, nil),
			token:   alice, version: mcpparser.MCPVersionLegacy,
		}
		fresh.warm(t, company.tool)
		assertCredentialToolHidden(t, fresh, delegatedTool, company.tool)
	})
	t.Run("row owned by another user", func(t *testing.T) {
		requireNoGitHubCall(t, backend)
		// The user-keyed writer refuses this owner mismatch, so plant it under
		// Alice's key directly in the backing store. The row is found (a missing
		// row would give the "requires upstream authorization" tool result) and
		// the call fails with the generic -32603. The user-keyed wrapper and the
		// token reader each compare the owner; each check has its own unit tests.
		require.NoError(t, f.as2Store.StoreUpstreamTokens(t.Context(), "user:"+user, delegatedGitHub,
			&storage.UpstreamTokens{UserID: "another-user", AccessToken: "gh-token-another", ExpiresAt: time.Now().Add(time.Hour)}))
		t.Cleanup(func() { require.NoError(t, store.StoreUpstreamTokens(userCtx, "", delegatedGitHub, row)) })

		assertCredentialUnavailable(t, owner.call(t, delegatedTool, nil))
	})
	t.Run("namespaced issuer does not grant credential access", func(t *testing.T) {
		requireNoGitHubCall(t, backend)
		claims := issuedTokenClaims(t, f.as1, aliceJAG)
		claims["iss"], claims["sub"], claims["jti"] = f.namespacedIssuer, delegatedAlice, rand.Text()
		delete(claims, "act")
		access := f.redeem(t, 0, signJWT(t, f.namespacedSigner, claims)).accessToken(t)
		issued := issuedTokenClaims(t, f.as2, access)
		assert.Equal(t, f.namespacedIssuer+"#"+delegatedAlice, issued["sub"])
		assert.NotContains(t, issued, claimnames.DelegatedAccessKey)
		// Use a sessionless request so session ownership cannot mask the
		// credential refusal. Neither provider can be discovered.
		caller := credentialRPC{handler: handler, token: access, version: mcpparser.MCPVersionModern}
		res := caller.post(t, "tools/list", "", nil)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		assertCredentialRPCError(t, res, -32603, "internal error")
	})
	t.Run("marked token at validator without audience", func(t *testing.T) {
		// A user-keyed reader without an audience is rejected at startup by
		// validateUserKeyedReader. With no reader, admission still fails closed.
		noAudience := f.incoming(t, "", nil)(handler)
		// The same validator accepts an unmarked AS-2 login token, so the
		// marker is what it refuses.
		control := credentialRPC{handler: noAudience, token: login, version: mcpparser.MCPVersionModern}
		require.Equal(t, http.StatusOK, control.post(t, "tools/list", "", nil).Code)
		requireNoGitHubCall(t, backend) // After the control, whose discovery reads Alice's credential.

		caller := owner
		caller.handler = noAudience
		res := caller.call(t, delegatedTool, nil)
		assert.Equal(t, http.StatusUnauthorized, res.Code, res.Body.String())
		assert.Contains(t, res.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
	})
}

// TestDelegatedAccessFirstConnect follows an agent's first use of a user who
// has never signed in at AS-2. The ID-JAG is refused as subject_unknown and is
// not consumed. Once Alice signs in (company IdP, then GitHub), the same ID-JAG
// resolves to the user that sign-in created, which owns her GitHub row, and the
// delegated call reaches GitHub with her token. A resolver that cached the
// refusal, or a resolver and a credential reader over different stores, would
// fail it.
func TestDelegatedAccessFirstConnect(t *testing.T) {
	skipWhileSubjectResolutionIsClosed(t)
	t.Parallel()
	f := startDelegatedFixture(t, "memory")
	jag := f.mint(t, 0, f.openGrant(t, f.login(t, f.as1Issuer, chainLoginAud, delegatedAlice, f.idp.Issuer())), "agent-1")
	refusal := f.redeem(t, 0, jag)
	refusal.refused(t, "access_denied", "subject_unknown")
	assert.Equal(t, "subject_unknown", refusal.body["error_description"])

	login := f.login(t, f.as2Issuer, chainResource, delegatedAlice, f.idp.Issuer(), f.githubURL)
	user, ok := issuedTokenClaims(t, f.as2, login)["sub"].(string)
	require.True(t, ok)
	row, err := f.as2.IDPTokenStorage().GetUpstreamTokens(identity.WithCanonicalUser(t.Context(), user), "", delegatedGitHub)
	require.NoError(t, err)
	assert.Equal(t, user, row.UserID)

	delegated := f.redeem(t, 0, jag).accessToken(t)
	assertDelegatedToken(t, f.as2, delegated, user, chainAS2Client, "agent-1")
	gateway := f.gateway(t)
	agent := credentialRPC{handler: gateway.handler(t, nil), token: delegated, version: mcpparser.MCPVersionLegacy}
	agent.warm(t, delegatedTool)
	agent.requireSuccess(t, delegatedTool, nil)
	assertCredentialHeaders(t, gateway.github.headers(), delegatedGitToken)
}

// TestDelegatedAccessRefreshesExpiredGitHubRow proves the delegated read
// refreshes Alice's expired GitHub row once, with the refresh token GitHub
// issued last, and writes the rotated tokens back to the row under her user
// key. The row is the one her interactive login uses too: each route gets the
// token the other refreshed and does not refresh it again. A delegated token
// carries no tsid, so the validator never bulk-loads upstream tokens for it and
// Identity.UpstreamTokens stays empty; the interactive login token, which does
// both, is the control. It runs on memory and on Redis storage.
func TestDelegatedAccessRefreshesExpiredGitHubRow(t *testing.T) {
	skipWhileSubjectResolutionIsClosed(t)
	t.Parallel()
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			t.Parallel()
			f := startDelegatedFixture(t, backend)
			user, login, delegated := f.connectAlice(t)
			store := f.as2.IDPTokenStorage()
			userCtx := identity.WithCanonicalUser(t.Context(), user)
			gateway := f.gateway(t)
			observed := func(next http.Handler) http.Handler {
				return gateway.incoming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id, _ := auth.IdentityFromContext(r.Context())
					if assert.NotNil(t, id) {
						switch id.AdmissionRoute {
						case auth.AdmissionRouteDelegated:
							assert.Nil(t, id.UpstreamTokens, "a delegated token must not fill Identity.UpstreamTokens")
						case auth.AdmissionRouteInteractive:
							assert.NotEmpty(t, id.UpstreamTokens)
						case auth.AdmissionRouteUnspecified:
							t.Errorf("unexpected admission route %q", id.AdmissionRoute)
						}
					}
					next.ServeHTTP(w, r)
				}))
			}
			handler := gateway.handler(t, observed)
			agent := credentialRPC{handler: handler, token: delegated, version: mcpparser.MCPVersionLegacy}
			agent.warm(t, delegatedTool, gateway.company.tool)
			interactive := credentialRPC{handler: handler, token: login, version: mcpparser.MCPVersionModern}

			expire := func() {
				t.Helper()
				row, err := store.GetUpstreamTokens(userCtx, "", delegatedGitHub)
				require.NoError(t, err)
				expired := *row
				expired.ExpiresAt = time.Now().Add(-time.Minute)
				require.NoError(t, store.StoreUpstreamTokens(userCtx, "", delegatedGitHub, &expired))
			}
			// callWith calls the GitHub tool as caller and checks every GitHub
			// backend request it caused, discovery included, carried token.
			callWith := func(caller *credentialRPC, token string) {
				t.Helper()
				before := len(gateway.github.headers())
				if caller.version == mcpparser.MCPVersionModern {
					caller.warm(t, delegatedTool)
				}
				caller.requireSuccess(t, delegatedTool, nil)
				assertCredentialHeaders(t, gateway.github.headers()[before:], token)
			}
			requireRow := func(access, refresh string) {
				t.Helper()
				row, err := store.GetUpstreamTokens(userCtx, "", delegatedGitHub)
				require.NoError(t, err)
				assert.Equal(t, access, row.AccessToken)
				assert.Equal(t, refresh, row.RefreshToken)
				assert.Equal(t, user, row.UserID)
				assert.True(t, row.ExpiresAt.After(time.Now()), "the refreshed row must not be expired")
			}

			require.Zero(t, gateway.bulkLoads.calls.Load(), "delegated calls so far must not bulk-load upstream tokens")
			expire()
			callWith(&agent, delegatedGitToken+"-r1")
			assert.Equal(t, []string{delegatedGitRefresh}, f.github.refreshes())
			requireRow(delegatedGitToken+"-r1", delegatedGitRefresh+"-r1")
			callWith(&agent, delegatedGitToken+"-r1")
			require.Zero(t, gateway.bulkLoads.calls.Load(), "delegated calls must not bulk-load upstream tokens")
			callWith(&interactive, delegatedGitToken+"-r1")
			assert.Positive(t, gateway.bulkLoads.calls.Load(), "the interactive route bulk-loads upstream tokens")
			assert.Len(t, f.github.refreshes(), 1, "a live row must not be refreshed again")

			expire()
			callWith(&interactive, delegatedGitToken+"-r2")
			callWith(&agent, delegatedGitToken+"-r2")
			assert.Equal(t, []string{delegatedGitRefresh, delegatedGitRefresh + "-r1"}, f.github.refreshes())
			requireRow(delegatedGitToken+"-r2", delegatedGitRefresh+"-r2")
		})
	}
}

// skipWhileSubjectResolutionIsClosed skips a test that needs a redemption from a
// resolve issuer. Those redemptions are refused until "Delegated access: turn on
// subject resolution at ID-JAG redemption" (#7247) lands, and that change
// removes this helper and its calls.
func skipWhileSubjectResolutionIsClosed(t *testing.T) {
	t.Helper()
	t.Skip("subject resolution at ID-JAG redemption is not turned on yet")
}

func requireNoGitHubCall(t *testing.T, backend *credentialBackend) {
	t.Helper()
	before := len(backend.headers())
	t.Cleanup(func() { assert.Len(t, backend.headers(), before, "refusal must not reach GitHub") })
}

func assertDelegatedToken(t *testing.T, as *runner.EmbeddedAuthServer, token, user, client, agent string) {
	t.Helper()
	claims := issuedTokenClaims(t, as, token)
	assert.Equal(t, user, claims["sub"])
	assert.Equal(t, client, claims["client_id"])
	assert.Equal(t, true, claims[claimnames.DelegatedAccessKey])
	assert.Equal(t, true, claims[claimnames.NoUpstreamSessionKey])
	assert.NotContains(t, claims, "tsid")
	assert.Equal(t, map[string]any{"iss": delegatedActIssuer, "sub": agent}, claims["act"])
}

type delegatedFixture struct {
	as1, as2                    *runner.EmbeddedAuthServer
	as1Issuer, as2Issuer        string
	idp                         *mockoidc.MockOIDC
	githubURL, namespacedIssuer string
	namespacedSigner            jose.Signer
	brokers, clients            [2]signedClient
	github                      *githubUpstream
	// as2Store is AS-2's backing store, below the user-keyed wrapper that
	// as2.IDPTokenStorage returns.
	as2Store storage.Storage
}

// startDelegatedFixture starts AS-1 and AS-2 with AS-2 on the named storage
// backend, "memory" or "redis" (miniredis). It builds both configs from
// chainAS1Config and chainAS2Config, the vMCP-level counterpart of
// pkg/authserver/two_server_id_jag_integration_test.go, and the AS-2
// trust-AS-1 policy shape is meant to stay in step with that test.
func startDelegatedFixture(t *testing.T, backend string) *delegatedFixture {
	t.Helper()
	l1, l2 := chainListen(t), chainListen(t)
	f := &delegatedFixture{as1Issuer: "http://" + l1.Addr().String(), as2Issuer: "http://" + l2.Addr().String()}
	var err error
	f.idp, err = mockoidc.Run()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, f.idp.Shutdown()) })
	dir := t.TempDir()
	secret := filepath.Join(dir, "company-secret")
	require.NoError(t, os.WriteFile(secret, []byte(f.idp.Config().ClientSecret), 0o600))
	f.github = startGitHubUpstream(t)
	f.githubURL = f.github.url

	key, jwksPath := chainClientKey(t, dir, "namespaced")
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, jwksPath)
	})
	other := httptest.NewServer(mux)
	t.Cleanup(other.Close)
	f.namespacedIssuer = other.URL
	f.namespacedSigner = joseSigner(t, jose.ES256, key, "namespaced", oauthproto.JWTTypeIDJAG)

	company := func(issuer string) authserver.UpstreamRunConfig {
		return authserver.UpstreamRunConfig{
			Name: delegatedCompany, Type: authserver.UpstreamProviderTypeOIDC,
			OIDCConfig: &authserver.OIDCUpstreamRunConfig{
				IssuerURL: f.idp.Issuer(), ClientID: f.idp.Config().ClientID, ClientSecretFile: secret,
				RedirectURI: issuer + "/oauth/callback", Scopes: []string{"openid", "profile", "email"},
				SubjectClaim: "sub", InsecureAllowHTTP: true, AllowPrivateIPs: true,
			},
		}
	}
	brokerA, brokerACfg := newSignedClient(t, dir, chainBroker,
		oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken, oauthproto.GrantTypeTokenExchange)
	brokerB, brokerBCfg := newSignedClient(t, dir, "broker-client-b",
		oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken, oauthproto.GrantTypeTokenExchange)
	clientA, clientACfg := newSignedClient(t, dir, chainAS2Client, oauthproto.GrantTypeJWTBearer, oauthproto.GrantTypeTokenExchange)
	clientB, clientBCfg := newSignedClient(t, dir, "client-b", oauthproto.GrantTypeJWTBearer, oauthproto.GrantTypeTokenExchange)
	f.brokers, f.clients = [2]signedClient{brokerA, brokerB}, [2]signedClient{clientA, clientB}

	cfg1 := chainAS1Config(f.as1Issuer, f.as2Issuer, brokerACfg.JWKSFile)
	cfg1.Upstreams = []authserver.UpstreamRunConfig{company(f.as1Issuer)}
	// A second AS-1 broker is the first one of a fresh config, renamed and
	// pointed at the second AS-2 client.
	brokerBConf := chainAS1Config(f.as1Issuer, f.as2Issuer, brokerBCfg.JWKSFile).InboundGrants.TokenExchange.DelegateClients[0]
	brokerBConf.ClientID = brokerB.id
	brokerBConf.TokenExchange.IDJAG.Targets[0].ClientID = clientB.id
	brokers := append(cfg1.InboundGrants.TokenExchange.DelegateClients, brokerBConf)
	for i := range brokers {
		brokers[i].TokenExchange.ExchangeRefreshTokens = &authserver.DelegateClientExchangeRefreshTokens{IDJAGMaxLifetime: "1h"}
		brokers[i].TokenExchange.ActorProfile = &authserver.DelegateClientActorProfile{
			Validator: "delegated-actor", ActorTokenType: oauthproto.TokenTypeJWT,
		}
		target := &brokers[i].TokenExchange.IDJAG.Targets[0]
		target.Lifetime = "5m"
		target.RequiredSubjectClaims = []string{tokenexchange.IDJAGSubjectClaimUpstreamSubject}
	}
	cfg1.InboundGrants.TokenExchange.DelegateClients = brokers

	cfg2 := chainAS2Config(f.as2Issuer, f.as1Issuer, clientACfg.JWKSFile, "")
	gh := helpers.NewOAuth2Upstream(delegatedGitHub, f.githubURL)
	gh.OAuth2Config.RedirectURI = f.as2Issuer + "/oauth/callback"
	gh.OAuth2Config.InsecureAllowHTTP, gh.OAuth2Config.AllowPrivateIPs = true, true
	cfg2.Upstreams = []authserver.UpstreamRunConfig{company(f.as2Issuer), gh}
	clientBCfg.Audiences = []string{chainResource}
	clientBCfg.TokenExchange = &authserver.DelegateClientTokenExchange{SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensAny}
	cfg2.InboundGrants.TokenExchange.DelegateClients = append(cfg2.InboundGrants.TokenExchange.DelegateClients, clientBCfg)
	policy := &cfg2.InboundGrants.JWTBearer.IssuerPolicies[0]
	policy.SubjectMode, policy.SubjectBindings = tokenexchange.SubjectModeResolve, nil
	policy.ActorIssuers = []string{delegatedActIssuer}
	policy.AcceptedAssertionTypes = []tokenexchange.JWTBearerAssertionType{tokenexchange.JWTBearerAssertionTypeIDJAG}
	cfg2.TrustedIssuers = append(cfg2.TrustedIssuers, tokenexchange.TrustedIssuer{
		Name: "namespaced", IssuerURL: other.URL, JWKSURL: other.URL + "/.well-known/jwks.json",
		InsecureAllowHTTP: true, AllowPrivateIPs: true,
	})
	policy.MaxAssertionAge = "5m"
	cfg2.InboundGrants.JWTBearer.IssuerPolicies = append(cfg2.InboundGrants.JWTBearer.IssuerPolicies, authserver.JWTBearerIssuerPolicyRunConfig{
		IssuerRef: "namespaced", MaxAssertionAge: "5m", SubjectMode: tokenexchange.SubjectModeNamespaced,
		AcceptedAudiences: []string{f.as2Issuer}, AcceptedAssertionTypes: policy.AcceptedAssertionTypes,
		SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{Subject: delegatedAlice, AllowedResources: []string{chainResource}}},
	})
	validator := actormocks.NewMockActorTokenValidator(gomock.NewController(t))
	validator.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, req tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			return tokenexchange.ActorResult{AgentID: req.ActorToken, ExpiresAt: time.Now().Add(time.Hour)}, nil
		}).AnyTimes()
	f.as1 = newTestAuthServer(t, cfg1, chainLoginAud, newDelegatedStorage(t, "memory"), runner.WithSubjectIdentifierAudiences([]string{chainLoginAud}),
		runner.WithActorTokenValidators(map[string]tokenexchange.ActorValidatorRegistration{
			"delegated-actor": {Validator: validator, ActIssuer: delegatedActIssuer},
		}))
	f.as2Store = newDelegatedStorage(t, backend)
	f.as2 = newTestAuthServer(t, cfg2, chainResource, f.as2Store, runner.WithUserKeyedUpstreamStorage())
	chainServe(t, l1, f.as1.Handler())
	chainServe(t, l2, f.as2.Handler())
	return f
}

func newDelegatedStorage(t *testing.T, backend string) storage.Storage {
	t.Helper()
	if backend == "redis" {
		client := redis.NewClient(&redis.Options{Addr: miniredis.RunT(t).Addr()})
		t.Cleanup(func() { assert.NoError(t, client.Close()) })
		return storage.NewRedisStorageWithClient(client, "delegated:{as2}:")
	}
	require.Equal(t, "memory", backend)
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { assert.NoError(t, stor.Close()) })
	return stor
}

func newTestAuthServer(
	t *testing.T, cfg *authserver.RunConfig, audience string, stor storage.Storage, opts ...runner.EmbeddedAuthServerOption,
) *runner.EmbeddedAuthServer {
	t.Helper()
	require.NoError(t, stor.RegisterClient(t.Context(), &fosite.DefaultClient{
		ID: chainSourceApp, Public: true, RedirectURIs: []string{chainRedirect}, ResponseTypes: []string{"code"},
		GrantTypes: []string{oauthproto.GrantTypeAuthorizationCode},
		Scopes:     []string{"openid", "profile", "offline_access"}, Audience: []string{audience},
	}))
	as, err := runner.NewEmbeddedAuthServerWithStorage(t.Context(), cfg, stor, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, as.Close()) })
	return as
}

// githubUpstream is a mock GitHub OAuth app for Alice. The authorization code
// grant issues delegatedGitToken and gh-refresh-alice. Each refresh grant must
// present the latest refresh token and rotates both tokens: refresh n issues
// gh-token-alice-r<n> and gh-refresh-alice-r<n>.
type githubUpstream struct {
	url       string
	mu        sync.Mutex
	refreshed []string // refresh tokens of successful refresh grants, in order
	presented []string // every refresh token presented, accepted or not, in order
}

func startGitHubUpstream(t *testing.T) *githubUpstream {
	t.Helper()
	g := &githubUpstream{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		callback, err := url.Parse(r.URL.Query().Get("redirect_uri"))
		if err != nil || callback.Path != "/oauth/callback" || callback.Hostname() != "127.0.0.1" {
			http.Error(w, "unexpected callback", http.StatusBadRequest)
			return
		}
		callback.RawQuery = url.Values{"code": {"github-alice-code"}, "state": {r.URL.Query().Get("state")}}.Encode()
		http.Redirect(w, r, callback.String(), http.StatusFound)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		access, refresh, ok := g.grant(r.FormValue("grant_type"), r.FormValue("code"), r.FormValue("refresh_token"))
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"refresh_token":%q,"token_type":"Bearer","expires_in":3600}`, access, refresh)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	g.url = server.URL
	return g
}

func (g *githubUpstream) grant(grantType, code, refreshToken string) (string, string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	switch {
	case grantType == oauthproto.GrantTypeAuthorizationCode && code == "github-alice-code":
		return delegatedGitToken, delegatedGitRefresh, true
	case grantType == oauthproto.GrantTypeRefreshToken && g.present(refreshToken) == g.latestRefresh():
		g.refreshed = append(g.refreshed, refreshToken)
		n := len(g.refreshed)
		return fmt.Sprintf("%s-r%d", delegatedGitToken, n), fmt.Sprintf("%s-r%d", delegatedGitRefresh, n), true
	default:
		return "", "", false
	}
}

// present records a presented refresh token and returns it. g.mu must be held.
func (g *githubUpstream) present(refreshToken string) string {
	g.presented = append(g.presented, refreshToken)
	return refreshToken
}

// latestRefresh is the refresh token the last grant issued. g.mu must be held.
func (g *githubUpstream) latestRefresh() string {
	if len(g.refreshed) == 0 {
		return delegatedGitRefresh
	}
	return fmt.Sprintf("%s-r%d", delegatedGitRefresh, len(g.refreshed))
}

// refreshes returns every refresh token presented so far, in order, including
// any GitHub rejected as stale.
func (g *githubUpstream) refreshes() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.presented...)
}

func (f *delegatedFixture) login(t *testing.T, issuer, audience, subject string, upstreams ...string) string {
	t.Helper()
	f.idp.QueueUser(&mockoidc.MockUser{Subject: subject, Email: subject + "@company.example"})
	browser := helpers.NewOAuthClient(issuer)
	upstreamBrowser := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	verifier, state := servercrypto.GeneratePKCEVerifier(), rand.Text()
	page, err := browser.StartAuthorization(url.Values{
		"client_id": {chainSourceApp}, "redirect_uri": {chainRedirect}, "response_type": {"code"},
		"scope": {"openid profile offline_access"}, "state": {state}, "resource": {audience},
		"code_challenge": {servercrypto.ComputePKCEChallenge(verifier)}, "code_challenge_method": {"S256"},
	})
	require.NoError(t, err)
	defer page.Body.Close()
	require.Equal(t, http.StatusOK, page.StatusCode)
	approved, err := browser.ApproveConsent(page, issuer)
	require.NoError(t, err)
	location := redirectLocation(t, approved, http.StatusSeeOther)
	for i, upstream := range upstreams {
		require.Equal(t, upstream+"/authorize", location.Scheme+"://"+location.Host+location.Path)
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, location.String(), nil)
		require.NoError(t, err)
		response, err := upstreamBrowser.Do(req)
		require.NoError(t, err)
		callback := redirectLocation(t, response, http.StatusFound)
		code, upstreamState := callback.Query().Get("code"), callback.Query().Get("state")
		require.NotEmpty(t, code, callback.String())
		require.NotEmpty(t, upstreamState)
		result, err := browser.Callback(code, upstreamState)
		require.NoError(t, err)
		status := http.StatusFound
		if i == len(upstreams)-1 {
			status = http.StatusSeeOther
		}
		location = redirectLocation(t, result, status)
	}
	assert.Equal(t, chainRedirect, location.Scheme+"://"+location.Host+location.Path)
	require.Equal(t, state, location.Query().Get("state"))
	code := location.Query().Get("code")
	require.NotEmpty(t, code, location.String())
	body, status, err := browser.ExchangeToken(url.Values{
		"grant_type": {oauthproto.GrantTypeAuthorizationCode}, "code": {code}, "redirect_uri": {chainRedirect},
		"code_verifier": {verifier}, "client_id": {chainSourceApp}, "resource": {audience},
	})
	require.NoError(t, err)
	return (tokenResponse{status, body}).accessToken(t)
}

func redirectLocation(t *testing.T, res *http.Response, status int) *url.URL {
	t.Helper()
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Equal(t, status, res.StatusCode, string(body))
	loc, err := res.Location()
	require.NoError(t, err)
	return loc
}

type signedClient struct {
	id     string
	signer jose.Signer
}

func newSignedClient(t *testing.T, dir, id string, grants ...string) (signedClient, authserver.DelegateClientRunConfig) {
	t.Helper()
	key, path := chainClientKey(t, dir, id)
	return signedClient{id, joseSigner(t, jose.ES256, key, id, "JWT")}, authserver.DelegateClientRunConfig{
		ClientID: id, JWKSFile: path, TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		TokenEndpointAuthSigningAlg: "ES256", GrantTypes: grants, Scopes: []string{"openid", "profile", "offline_access"},
	}
}

func (c signedClient) post(t *testing.T, issuer string, form url.Values) tokenResponse {
	t.Helper()
	params := url.Values{
		"client_id": {c.id}, "client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"},
	}
	for k, v := range form {
		params[k] = v
	}
	now := time.Now()
	params.Set("client_assertion", signJWT(t, c.signer, jwt.Claims{
		Issuer: c.id, Subject: c.id, Audience: jwt.Audience{issuer}, ID: rand.Text(),
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
	}))
	body, status, err := helpers.NewOAuthClient(issuer).ExchangeToken(params)
	require.NoError(t, err)
	return tokenResponse{status, body}
}

type tokenResponse struct {
	status int
	body   map[string]any
}

func (r tokenResponse) accessToken(t *testing.T) string {
	t.Helper()
	require.Equal(t, http.StatusOK, r.status, r.body)
	token, _ := r.body["access_token"].(string)
	require.NotEmpty(t, token)
	return token
}

func (r tokenResponse) refused(t *testing.T, code, descriptionPrefix string) {
	t.Helper()
	require.Equal(t, http.StatusBadRequest, r.status, r.body)
	assert.Equal(t, code, r.body["error"])
	description, ok := r.body["error_description"].(string)
	require.True(t, ok, r.body)
	assert.True(t, strings.HasPrefix(description, descriptionPrefix), "unexpected error description: %q", description)
	assert.NotContains(t, r.body, "access_token")
}

func (f *delegatedFixture) openGrant(t *testing.T, login string) string {
	t.Helper()
	return f.brokers[0].openGrant(t, f.as1Issuer, login)
}

func (c signedClient) openGrant(t *testing.T, issuer, login string) string {
	t.Helper()
	res := c.post(t, issuer, url.Values{
		"grant_type": {oauthproto.GrantTypeTokenExchange}, "subject_token": {login},
		"subject_token_type": {oauthproto.TokenTypeAccessToken}, "requested_token_type": {oauthproto.TokenTypeRefreshToken},
		"authorization_details": {chainGrantEntry},
	})
	token := res.accessToken(t)
	require.Equal(t, oauthproto.TokenTypeRefreshToken, res.body["issued_token_type"])
	return token
}

func (f *delegatedFixture) mint(t *testing.T, client int, grant, agent string) string {
	t.Helper()
	res := f.brokers[client].post(t, f.as1Issuer, url.Values{
		"grant_type": {oauthproto.GrantTypeTokenExchange}, "subject_token": {grant},
		"subject_token_type": {oauthproto.TokenTypeRefreshToken}, "requested_token_type": {oauthproto.TokenTypeIDJAG},
		"audience": {f.as2Issuer}, "resource": {chainResource},
		"actor_token": {agent}, "actor_token_type": {oauthproto.TokenTypeJWT},
	})
	token := res.accessToken(t)
	require.Equal(t, oauthproto.TokenTypeIDJAG, res.body["issued_token_type"])
	return token
}

func (f *delegatedFixture) redeem(t *testing.T, client int, assertion string) tokenResponse {
	t.Helper()
	return f.clients[client].post(t, f.as2Issuer, url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer}, "assertion": {assertion},
	})
}

func (f *delegatedFixture) incoming(t *testing.T, audience string, reader upstreamtoken.TokenReader) func(http.Handler) http.Handler {
	t.Helper()
	incoming, _, _, err := authfactory.NewIncomingAuthMiddleware(t.Context(), &config.IncomingAuthConfig{
		Type: config.IncomingAuthTypeOIDC,
		OIDC: &config.OIDCConfig{
			Issuer: f.as2Issuer, Audience: audience, Resource: chainResource,
			JWKSURL: f.as2Issuer + "/.well-known/jwks.json", InsecureAllowHTTP: true, JwksAllowPrivateIP: true,
		},
	}, "delegated-access", nil, reader, f.as2.KeyProvider(), f.as2Issuer)
	require.NoError(t, err)
	return incoming
}

// delegatedGateway is what a vMCP in front of AS-2 needs: an incoming validator
// and an upstream-inject strategy that both read AS-2's user-keyed store, a
// GitHub backend and a company backend. The company backend keeps discovery
// working when Alice's GitHub credential is unusable.
type delegatedGateway struct {
	incoming        func(http.Handler) http.Handler
	inject          vmcpauth.Strategy
	github, company *credentialBackend
	// bulkLoads counts the validator's bulk upstream reads.
	bulkLoads *bulkLoadCounter
}

// bulkLoadCounter wraps the reader the incoming validator is built with and
// counts GetAllUpstreamCredentials calls, the validator's bulk load.
type bulkLoadCounter struct {
	upstreamtoken.TokenReader
	calls atomic.Int64
}

func (c *bulkLoadCounter) GetAllUpstreamCredentials(
	ctx context.Context, sessionID string,
) (map[string]upstreamtoken.UpstreamCredential, map[string]error, error) {
	c.calls.Add(1)
	return c.TokenReader.GetAllUpstreamCredentials(ctx, sessionID)
}

func (f *delegatedFixture) gateway(t *testing.T) *delegatedGateway {
	t.Helper()
	reader := upstreamtoken.NewInProcessService(f.as2.IDPTokenStorage(), f.as2.UpstreamTokenRefresher())
	inject := strategies.NewUpstreamInjectStrategy(strategies.WithUserCredentialReader(reader))
	bulk := &bulkLoadCounter{TokenReader: reader}
	return &delegatedGateway{
		incoming: f.incoming(t, chainResource, bulk), inject: inject, bulkLoads: bulk,
		github:  newCredentialBackend(t, delegatedGitHub, delegatedTool),
		company: newCredentialBackend(t, delegatedCompany, "company_search"),
	}
}

// handler starts a vMCP behind incoming, or behind the gateway's own incoming
// validator when incoming is nil.
func (g *delegatedGateway) handler(t *testing.T, incoming func(http.Handler) http.Handler) http.Handler {
	t.Helper()
	if incoming == nil {
		incoming = g.incoming
	}
	return newCredentialVMCP(t, incoming, g.inject, nil, g.github.backend, g.company.backend)
}

// connectAlice signs Alice in at AS-1 and at AS-2 (company IdP, then GitHub)
// and redeems an ID-JAG for agent-1. It returns her local AS-2 user, her AS-2
// login token and the delegated token.
func (f *delegatedFixture) connectAlice(t *testing.T) (user, login, delegated string) {
	t.Helper()
	jag := f.mint(t, 0, f.openGrant(t, f.login(t, f.as1Issuer, chainLoginAud, delegatedAlice, f.idp.Issuer())), "agent-1")
	login = f.login(t, f.as2Issuer, chainResource, delegatedAlice, f.idp.Issuer(), f.githubURL)
	user, ok := issuedTokenClaims(t, f.as2, login)["sub"].(string)
	require.True(t, ok)
	delegated = f.redeem(t, 0, jag).accessToken(t)
	assertDelegatedToken(t, f.as2, delegated, user, chainAS2Client, "agent-1")
	return user, login, delegated
}

func joseSigner(t *testing.T, alg jose.SignatureAlgorithm, key any, kid, typ string) jose.Signer {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: alg, Key: key},
		(&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", kid))
	require.NoError(t, err)
	return signer
}

func signJWT(t *testing.T, signer jose.Signer, claims any) string {
	t.Helper()
	token, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return token
}

func issuedTokenClaims(t *testing.T, as *runner.EmbeddedAuthServer, token string) map[string]any {
	t.Helper()
	key, err := as.KeyProvider().SigningKey(t.Context())
	require.NoError(t, err)
	parsed, err := jose.ParseSignedCompact(token, []jose.SignatureAlgorithm{jose.SignatureAlgorithm(key.Algorithm)})
	require.NoError(t, err)
	jwk := jose.JSONWebKey{Key: key.Key}
	public := jwk.Public()
	payload, err := parsed.Verify(public.Key)
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims
}
