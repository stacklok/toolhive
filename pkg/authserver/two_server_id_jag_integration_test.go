// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// as2Env is an embedded auth server that redeems ID-JAGs minted by AS-1.
type as2Env struct {
	srv      *httptest.Server
	issuer   string
	embedded *authserverrunner.EmbeddedAuthServer
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
// grant, asking for resource.
func (e *as2Env) redeem(t *testing.T, auth clientAuth, assertion, resource string) tokenResult {
	t.Helper()
	return postToken(t, e.srv.URL, auth, url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer},
		"assertion":  {assertion},
		"resource":   {resource},
	})
}

// startAS2 starts an AS-2 on listener that trusts the AS-1 at as1Issuer. AS-1's
// JWKS is fetched at the first redemption, so AS-1 may start later. user-1 is
// AS-1's sub for the grant's user, bound exactly. The server's own issuer is an
// accepted audience and is not an allowed audience; the resource is.
func startAS2(t *testing.T, listener net.Listener, as1Issuer string) *as2Env {
	t.Helper()
	key := newAssertionKey(t, "as2-client-key")
	jwksFile := filepath.Join(t.TempDir(), "as2-client.jwks")
	writeJWKS(t, jwksFile, key)

	resources := []string{xrtIDJAGResource}
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
					Subject: "user-1", AllowedResources: resources,
				}},
				AcceptedAudiences: []string{issuer},
			}},
		}
	}, nil)
	return &as2Env{
		srv: srv, issuer: issuer, embedded: embedded,
		client: clientAuth{clientID: xrtTargetClientID, key: &key},
	}
}

// startTwoServerIDJAG is the one two-server harness in this package: AS-1
// mints ID-JAGs and AS-2 trusts AS-1 and redeems them, in one process, over
// HTTP, on memory storage. It binds both listeners first, so AS-2 can be given
// AS-1's issuer and AS-1 can be given AS-2's, then starts AS-2 and AS-1. AS-1's
// broker has one ID-JAG target: as2, with one resource.
func startTwoServerIDJAG(t *testing.T) *twoServerIDJAG {
	t.Helper()
	listen := func() net.Listener {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		// Closes a listener whose server never started. After a server has
		// stopped, the second close fails harmlessly and the error is ignored.
		t.Cleanup(func() { _ = listener.Close() })
		return listener
	}
	l1, l2 := listen(), listen()
	as1Issuer := "http://" + l1.Addr().String()

	as2 := startAS2(t, l2, as1Issuer)

	as1 := startXRTServerOn(t, "memory", l1, func(cfg *authserver.RunConfig, _ string) {
		for _, delegate := range cfg.InboundGrants.TokenExchange.DelegateClients {
			if delegate.ClientID != cfgBrokerID {
				continue
			}
			delegate.TokenExchange.IDJAG.Targets = []authserver.DelegateClientIDJAGTarget{xrtIDJAGTarget(as2.issuer)}
		}
	})
	return &twoServerIDJAG{as1: as1, as2: as2}
}

// mintIDJAG mints an ID-JAG for AS-2 from a fresh grant for user-1.
func (h *twoServerIDJAG) mintIDJAG(t *testing.T) string {
	t.Helper()
	grant := h.as1.idJAGGrant(t, h.as1.broker)
	res := h.as1.requestIDJAG(t, h.as1.broker, grant, h.as2.issuer)
	require.Equal(t, http.StatusOK, res.status, res.body)
	return res.str(t, "access_token")
}

// TestIDJAGRedemptionAcrossTwoServers proves AS-1 mints an ID-JAG that AS-2
// redeems for an access token.
func TestIDJAGRedemptionAcrossTwoServers(t *testing.T) {
	t.Parallel()
	h := startTwoServerIDJAG(t)

	res := h.as2.redeem(t, h.as2.client, h.mintIDJAG(t), xrtIDJAGResource)
	require.Equal(t, http.StatusOK, res.status, res.body)
	access := accessTokenClaims(t, h.as2.embedded, res.str(t, "access_token"))
	assert.Equal(t, h.as1.issuer+"#user-1", access["sub"], "sub is <AS-1 iss>#<sub>")
	assert.Equal(t, h.as2.issuer, access["iss"])
	assert.Equal(t, []any{xrtIDJAGResource}, access["aud"])
	assert.Equal(t, xrtTargetClientID, access["client_id"])
	assert.NotContains(t, access, "act")
}
