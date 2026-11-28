// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/oauthproto"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// The ID-JAG chain these tests run: AS-1 mints an ID-JAG with the
// mcp_tool_call entry signed into it, AS-2 redeems it for an access token
// that carries the entry, and the vMCP validates that token with the incoming
// OIDC validator production builds and enforces the entry. The names follow
// pkg/authserver/two_server_id_jag_integration_test.go.
const (
	chainResource   = "https://gw.example.net/mcp"
	chainBroker     = "configured-broker"
	chainSourceApp  = "dcr-app"
	chainAS2Client  = "broker-at-as2"
	chainLoginAud   = "https://delegate.test.local"
	chainBrokerAud  = "https://other.test.local"
	chainUser       = "user-1"
	chainRedirect   = "https://broker.example.com/callback"
	chainGrantEntry = `[{"type":"https://toolhive.dev/exchange_grant",` +
		`"issuable_token_types":["urn:ietf:params:oauth:token-type:id-jag"]}]`
)

// chainBackends are the vMCP's two backends. Each has one tool, prompt,
// resource and resource template, named after it.
var chainBackends = []string{"alpha", "beta"}

// idJAGChain is AS-1, AS-2 and a vMCP over the chainBackends, in one process
// over loopback HTTP.
type idJAGChain struct {
	as1URL    string
	as2URL    string
	as1       *authserverrunner.EmbeddedAuthServer
	brokerKey *ecdsa.PrivateKey
	as2Key    *ecdsa.PrivateKey
	vmcpURL   string
	// toolCalls counts the backend tool calls.
	toolCalls *atomic.Int32
	// hooks, when set, are the alpha backend's long-running tools.
	hooks *chainHooks
}

// chainWaitLimit bounds how long alpha_wait runs when it is not cancelled.
const chainWaitLimit = 20 * time.Second

// chainHooks drive the alpha backend's long-running tools: alpha_wait, which
// runs until it is cancelled or for chainWaitLimit, and alpha_ask, which asks the client a question
// once askNow is closed and answers with the client's action.
type chainHooks struct {
	waitStarted   chan struct{}
	waitCancelled chan struct{}
	askStarted    chan struct{}
	askNow        chan struct{}
}

// chainOptions vary the chain.
type chainOptions struct {
	// accessTokenLifespan, when set, is the lifetime of AS-2's access tokens.
	accessTokenLifespan string
	// longCalls adds the alpha backend's long-running tools (chainHooks).
	longCalls bool
	// expiredTokenGrace, when set, lets AS-2's tokens for chainAS2Client send
	// their call's messages for that long after exp (the vMCP's
	// ExpiredTokenAdmission list). Unset keeps exp strict.
	expiredTokenGrace time.Duration
}

func startIDJAGChain(t *testing.T) *idJAGChain {
	t.Helper()
	return startIDJAGChainWith(t, chainOptions{})
}

func startIDJAGChainWith(t *testing.T, opts chainOptions) *idJAGChain {
	t.Helper()
	ctx := t.Context()
	l1, l2 := chainListen(t), chainListen(t)
	c := &idJAGChain{
		as1URL: "http://" + l1.Addr().String(), as2URL: "http://" + l2.Addr().String(),
		toolCalls: &atomic.Int32{},
	}
	if opts.longCalls {
		c.hooks = &chainHooks{
			waitStarted: make(chan struct{}, 1), waitCancelled: make(chan struct{}, 1),
			askStarted: make(chan struct{}, 1), askNow: make(chan struct{}),
		}
	}
	dir := t.TempDir()
	var brokerJWKS, as2JWKS string
	c.brokerKey, brokerJWKS = chainClientKey(t, dir, "broker")
	c.as2Key, as2JWKS = chainClientKey(t, dir, "as2-client")

	as2, err := authserverrunner.NewEmbeddedAuthServerWithStorage(ctx,
		chainAS2Config(c.as2URL, c.as1URL, as2JWKS, opts.accessTokenLifespan), storage.NewMemoryStorage(),
		authserverrunner.WithAuthorizationDetailsRegistry(chainRegistry(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = as2.Close() })

	stor1 := storage.NewMemoryStorage()
	require.NoError(t, stor1.RegisterClient(ctx, &fosite.DefaultClient{
		ID: chainSourceApp, Public: true, RedirectURIs: []string{chainRedirect},
		ResponseTypes: []string{"code"},
		GrantTypes:    []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
		Scopes:        []string{"openid", "profile", "offline_access"}, Audience: []string{chainLoginAud},
	}))
	as1, err := authserverrunner.NewEmbeddedAuthServerWithStorage(ctx,
		chainAS1Config(c.as1URL, c.as2URL, brokerJWKS), stor1,
		authserverrunner.WithSubjectIdentifierAudiences([]string{chainLoginAud}),
		authserverrunner.WithAuthorizationDetailsRegistry(chainRegistry(t)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = as1.Close() })
	c.as1 = as1
	chainServe(t, l1, as1.Handler())
	chainServe(t, l2, as2.Handler())

	var admission []callbinding.ExpiredTokenAdmission
	if opts.expiredTokenGrace > 0 {
		admission = []callbinding.ExpiredTokenAdmission{
			{Issuer: c.as2URL, ClientID: chainAS2Client, Grace: opts.expiredTokenGrace},
		}
	}
	c.vmcpURL = startChainVMCP(t, c.as2URL, c.toolCalls, c.hooks, admission)
	return c
}

func chainListen(t *testing.T) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func chainServe(t *testing.T, l net.Listener, h http.Handler) {
	t.Helper()
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}}
	srv.Start()
	t.Cleanup(srv.Close)
}

// chainRegistry registers the MCP call type, as an embedder does on both
// authorization servers.
func chainRegistry(t *testing.T) *authorizationdetails.Registry {
	t.Helper()
	types := map[string]authorizationdetails.Type{}
	toolcalldetails.Register(types)
	registry, err := authorizationdetails.New(types)
	require.NoError(t, err)
	return registry
}

// chainClientKey makes an ES256 key and writes its public JWKS to a file.
func chainClientKey(t *testing.T, dir, name string) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	set, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &key.PublicKey, KeyID: name, Use: "sig", Algorithm: "ES256",
	}}})
	require.NoError(t, err)
	path := filepath.Join(dir, name+".jwks")
	require.NoError(t, os.WriteFile(path, set, 0o600))
	return key, path
}

// chainBaseConfig is an embedded server with one OAuth2 upstream that no test
// visits: no login runs through it.
func chainBaseConfig(issuer string) *authserver.RunConfig {
	const upstream = "http://127.0.0.1:9"
	return &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        issuer,
		Upstreams: []authserver.UpstreamRunConfig{{
			Name: "test-upstream",
			Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: upstream + "/authorize",
				TokenEndpoint:         upstream + "/token",
				ClientID:              "test-client-id",
				RedirectURI:           issuer + "/oauth/callback",
			},
		}},
		ScopesSupported:  []string{"openid", "profile", "offline_access"},
		AllowedAudiences: []string{"https://mcp.test.local"},
		InsecureAllowConfidentialOverLoopbackHTTP: true,
	}
}

// chainAS1Config lets the broker exchange dcr-app login tokens for exchange
// grants and redeem a grant for an ID-JAG for AS-2.
func chainAS1Config(issuer, as2Issuer, brokerJWKS string) *authserver.RunConfig {
	cfg := chainBaseConfig(issuer)
	cfg.AllowedAudiences = []string{chainLoginAud, chainBrokerAud}
	cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
		TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
			DelegateClients: []authserver.DelegateClientRunConfig{{
				ClientID:                    chainBroker,
				TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
				JWKSFile:                    brokerJWKS,
				TokenEndpointAuthSigningAlg: "ES256",
				GrantTypes: []string{
					oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken,
					oauthproto.GrantTypeTokenExchange,
				},
				RedirectURIs: []string{chainRedirect},
				Scopes:       []string{"openid", "profile", "offline_access"},
				Audiences:    []string{chainBrokerAud},
				TokenExchange: &authserver.DelegateClientTokenExchange{
					SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensRestricted,
					SubjectTokenAudiences:   []string{chainLoginAud},
					AllowedSourceClients:    []string{chainSourceApp},
					ExchangeRefreshTokens: &authserver.DelegateClientExchangeRefreshTokens{
						IDJAGMaxLifetime: "12h", RefreshTokenMaxLifetime: "720h",
					},
					IDJAG: &authserver.DelegateClientIDJAG{Targets: []authserver.DelegateClientIDJAGTarget{{
						Issuer: as2Issuer, ClientID: chainAS2Client, Resources: []string{chainResource},
					}}},
				},
			}},
		},
	}
	return cfg
}

// chainAS2Config trusts AS-1, binds AS-1's user to the vMCP's resource, and
// lets chainAS2Client redeem ID-JAGs addressed to AS-2, for access tokens of
// accessTokenLifespan when it is set.
func chainAS2Config(issuer, as1Issuer, clientJWKS, accessTokenLifespan string) *authserver.RunConfig {
	cfg := chainBaseConfig(issuer)
	resources := []string{chainResource}
	cfg.AllowedAudiences = resources
	cfg.InboundGrants = &authserver.InboundGrantsRunConfig{
		TokenExchange: &authserver.TokenExchangeInboundGrantRunConfig{
			DelegateClients: []authserver.DelegateClientRunConfig{{
				ClientID:                    chainAS2Client,
				TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
				JWKSFile:                    clientJWKS,
				TokenEndpointAuthSigningAlg: "ES256",
				GrantTypes:                  []string{oauthproto.GrantTypeTokenExchange, oauthproto.GrantTypeJWTBearer},
				Scopes:                      []string{"openid", "profile"},
				Audiences:                   resources,
				AccessTokenLifespan:         accessTokenLifespan,
				TokenExchange: &authserver.DelegateClientTokenExchange{
					SelfIssuedSubjectTokens: authserver.SelfIssuedSubjectTokensAny,
				},
			}},
		},
		JWTBearer: &authserver.JWTBearerInboundGrantRunConfig{
			IssuerPolicies: []authserver.JWTBearerIssuerPolicyRunConfig{{
				IssuerRef: "as1", MaxAssertionAge: "2m",
				SubjectBindings: []tokenexchange.JWTBearerSubjectBinding{{
					Subject: chainUser, AllowedResources: resources,
				}},
				AcceptedAudiences: []string{issuer},
			}},
		},
	}
	cfg.TrustedIssuers = []tokenexchange.TrustedIssuer{{
		Name: "as1", IssuerURL: as1Issuer, JWKSURL: as1Issuer + "/.well-known/jwks.json",
		InsecureAllowHTTP: true, AllowPrivateIPs: true,
	}}
	return cfg
}

// startChainBackend starts an MCP backend named id with one tool, prompt,
// resource and resource template, each named after id, and the long-running
// tools hooks drive on the first backend when hooks is set.
func startChainBackend(t *testing.T, id string, toolCalls *atomic.Int32, hooks *chainHooks) string {
	t.Helper()
	srv := mcpserver.NewMCPServer(id, "1.0.0")
	srv.AddTool(mcpmcp.NewTool(id+"_echo", mcpmcp.WithString("input", mcpmcp.Required())),
		func(_ context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			toolCalls.Add(1)
			args, _ := req.Params.Arguments.(map[string]any)
			input, _ := args["input"].(string)
			return &mcpmcp.CallToolResult{Content: []mcpmcp.Content{mcpmcp.NewTextContent(input)}}, nil
		})
	if hooks != nil && id == chainBackends[0] {
		addChainLongCalls(srv, id, hooks)
	}
	srv.AddPrompt(mcpmcp.NewPrompt("review"),
		func(context.Context, mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
			return &mcpmcp.GetPromptResult{}, nil
		})
	uri := "file:///" + id + "/readme"
	srv.AddResource(mcpmcp.Resource{URI: uri, Name: id + "_readme", MIMEType: "text/plain"},
		func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return []mcpmcp.ResourceContents{mcpmcp.TextResourceContents{URI: uri, Text: id}}, nil
		})
	srv.AddResourceTemplate(mcpmcp.ResourceTemplate{URITemplate: "file:///" + id + "/{path}", Name: id + "_files"},
		func(context.Context, mcpmcp.ReadResourceRequest) ([]mcpmcp.ResourceContents, error) {
			return nil, nil
		})
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.NewStreamableHTTPServer(srv))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL + "/mcp"
}

// addChainLongCalls adds id_wait and id_ask (see chainHooks) to srv.
func addChainLongCalls(srv *mcpserver.MCPServer, id string, hooks *chainHooks) {
	srv.AddTool(mcpmcp.NewTool(id+"_wait"),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			hooks.waitStarted <- struct{}{}
			// Bounded, so a cancellation that never reaches the backend does
			// not hold the test's servers open.
			select {
			case <-ctx.Done():
				hooks.waitCancelled <- struct{}{}
				return nil, ctx.Err()
			case <-time.After(chainWaitLimit):
				return &mcpmcp.CallToolResult{Content: []mcpmcp.Content{mcpmcp.NewTextContent("not cancelled")}}, nil
			}
		})
	srv.AddTool(mcpmcp.NewTool(id+"_ask"),
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			hooks.askStarted <- struct{}{}
			select {
			case <-hooks.askNow:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			res, err := srv.RequestElicitation(ctx, mcpmcp.ElicitationRequest{Params: mcpmcp.ElicitationParams{
				Message:         "Go ahead?",
				RequestedSchema: map[string]any{"type": "object", "properties": map[string]any{}},
			}})
			if err != nil {
				return nil, err
			}
			return &mcpmcp.CallToolResult{Content: []mcpmcp.Content{
				mcpmcp.NewTextContent("client said " + string(res.Action)),
			}}, nil
		})
}

// startChainVMCP builds a vMCP over the chainBackends whose incoming auth is
// the OIDC validator for AS-2's access tokens for chainResource, and that
// requires an entry from chainAS2Client's tokens. The validator and the
// server share one call binding, as the vmcp command wires them.
func startChainVMCP(
	t *testing.T, as2Issuer string, toolCalls *atomic.Int32, hooks *chainHooks,
	admission []callbinding.ExpiredTokenAdmission,
) string {
	t.Helper()
	ctrl := gomock.NewController(t)
	registry := mocks.NewMockBackendRegistry(ctrl)
	backends := make([]vmcp.Backend, 0, len(chainBackends))
	for _, id := range chainBackends {
		backends = append(backends, vmcp.Backend{
			ID: id, Name: id, BaseURL: startChainBackend(t, id, toolCalls, hooks), TransportType: "streamable-http",
		})
	}
	registry.EXPECT().List(gomock.Any()).Return(backends).AnyTimes()
	registry.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, id string) *vmcp.Backend {
		for i := range backends {
			if backends[i].ID == id {
				return &backends[i]
			}
		}
		return nil
	}).AnyTimes()

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, authReg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated,
		strategies.NewUnauthenticatedStrategy()))
	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(t, err)
	resolver, err := aggregator.NewPriorityConflictResolver(chainBackends)
	require.NoError(t, err)

	binding, err := callbinding.New(callbinding.Config{
		Clients: []string{chainAS2Client}, ExpiredTokenAdmission: admission,
	})
	require.NoError(t, err)
	authMw, _, _, err := authfactory.NewIncomingAuthMiddleware(t.Context(), &config.IncomingAuthConfig{
		Type: config.IncomingAuthTypeOIDC,
		OIDC: &config.OIDCConfig{
			Issuer: as2Issuer, Audience: chainResource, Resource: chainResource,
			JWKSURL: as2Issuer + "/.well-known/jwks.json", InsecureAllowHTTP: true, JwksAllowPrivateIP: true,
		},
	}, "test-vmcp", nil, nil, nil, "", authfactory.WithCallBinding(binding))
	require.NoError(t, err)

	srv, err := server.New(t.Context(), &server.Config{
		Name: "test-vmcp", Host: "127.0.0.1", Port: 0, SessionTTL: 5 * time.Minute,
		SessionFactory: vmcpsession.NewSessionFactory(authReg),
		Aggregator:     aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil),
		AuthMiddleware: authMw,
		CallBinding:    binding,
	}, router.NewSessionRouter(&vmcp.RoutingTable{}), backendClient, registry, nil)
	require.NoError(t, err)
	// Registered after the backends, so it runs before they close: stopping
	// the server closes its backend sessions and their streams.
	t.Cleanup(func() { _ = srv.Stop(context.Background()) })
	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts.URL
}

// accessToken runs the chain for one entry: a simulated login token for the
// user, an exchange grant from it at AS-1, an ID-JAG for AS-2 with entry from
// the grant, and AS-2's access token for the ID-JAG. It returns the access
// token.
func (c *idJAGChain) accessToken(t *testing.T, entry string) string {
	t.Helper()
	grant := c.token(t, c.as1URL, chainBroker, c.brokerKey, url.Values{
		"grant_type":            {oauthproto.GrantTypeTokenExchange},
		"subject_token":         {c.login(t)},
		"subject_token_type":    {oauthproto.TokenTypeAccessToken},
		"requested_token_type":  {oauthproto.TokenTypeRefreshToken},
		"authorization_details": {chainGrantEntry},
	})
	idJAG := c.token(t, c.as1URL, chainBroker, c.brokerKey, url.Values{
		"grant_type":            {oauthproto.GrantTypeTokenExchange},
		"subject_token":         {grant},
		"subject_token_type":    {oauthproto.TokenTypeRefreshToken},
		"requested_token_type":  {oauthproto.TokenTypeIDJAG},
		"audience":              {c.as2URL},
		"resource":              {chainResource},
		"authorization_details": {"[" + entry + "]"},
	})
	assert.JSONEq(t, "["+entry+"]", string(chainJWTClaim(t, idJAG, "authorization_details")),
		"AS-1 signs the entry into the ID-JAG")
	access := c.token(t, c.as2URL, chainAS2Client, c.as2Key, url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer},
		"assertion":  {idJAG},
		"resource":   {chainResource},
	})
	assert.JSONEq(t, "["+entry+"]", string(chainJWTClaim(t, access, "authorization_details")),
		"AS-2 copies the signed entry into the access token")
	return access
}

// login signs a login-shaped access token for the user, issued by AS-1 to
// dcr-app, with AS-1's own key. It stands in for a browser login.
func (c *idJAGChain) login(t *testing.T) string {
	t.Helper()
	key, err := c.as1.KeyProvider().SigningKey(t.Context())
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key.Key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", key.KeyID))
	require.NoError(t, err)
	now := time.Now()
	token, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: c.as1URL, Subject: chainUser, Audience: jwt.Audience{chainLoginAud}, ID: rand.Text(),
		Expiry: jwt.NewNumericDate(now.Add(30 * time.Minute)), IssuedAt: jwt.NewNumericDate(now),
	}).Claims(map[string]any{"client_id": chainSourceApp, "scope": "openid profile offline_access"}).Serialize()
	require.NoError(t, err)
	return token
}

// token posts form to issuer's token endpoint as client, authenticated with a
// private_key_jwt assertion, and returns the issued access_token.
func (*idJAGChain) token(t *testing.T, issuer, client string, key *ecdsa.PrivateKey, form url.Values) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	now := time.Now()
	assertion, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer: client, Subject: client, Audience: jwt.Audience{issuer}, ID: rand.Text(),
		IssuedAt: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(now.Add(time.Minute)),
	}).Serialize()
	require.NoError(t, err)
	form.Set("client_id", client)
	form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
	form.Set("client_assertion", assertion)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, issuer+"/oauth/token",
		strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, http.StatusOK, resp.StatusCode, "token endpoint at %s: %v", issuer, body)
	token, _ := body["access_token"].(string)
	require.NotEmpty(t, token)
	return token
}

// chainJWTClaim returns the raw bytes of one claim of a JWT, without
// verifying it.
func chainJWTClaim(t *testing.T, token, name string) json.RawMessage {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(payload, &claims))
	return claims[name]
}

// post sends a JSON-RPC body to the vMCP with token, on session when set,
// with extra headers, and returns the status, body and Mcp-Session-Id.
func (c *idJAGChain) post(t *testing.T, token, session, body string, extra http.Header) (int, string, string) {
	t.Helper()
	return cbPostMCP(t, c.vmcpURL, token, session, body, extra)
}

// session opens a Legacy session with token.
func (c *idJAGChain) session(t *testing.T, token string) string {
	t.Helper()
	return cbOpenSession(t, c.vmcpURL, token)
}

// chainListItems returns the keys of the items in a list response.
func chainListItems(t *testing.T, method, body string) []string {
	t.Helper()
	members := map[string][2]string{
		"tools/list":               {"tools", "name"},
		"prompts/list":             {"prompts", "name"},
		"resources/list":           {"resources", "uri"},
		"resources/templates/list": {"resourceTemplates", "uriTemplate"},
	}[method]
	var resp struct {
		Result map[string]json.RawMessage `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &resp), "body: %s", body)
	var items []map[string]any
	require.NoError(t, json.Unmarshal(resp.Result[members[0]], &items), "body: %s", body)
	keys := []string{}
	for _, item := range items {
		key, _ := item[members[1]].(string)
		keys = append(keys, key)
	}
	return keys
}

// chainListEntry is a list-call entry for backend and method.
func chainListEntry(backend, method string) string {
	return fmt.Sprintf(`{"type":%q,"server":%q,"method":%q}`, toolcalldetails.Type, backend, method)
}

// chainWant is the one item a list of method holds for the alpha backend.
var chainWant = map[string]string{
	"tools/list":               "alpha_echo",
	"prompts/list":             "alpha_review",
	"resources/list":           "file:///alpha/readme",
	"resources/templates/list": "file:///alpha/{path}",
}

// TestCallBindingIDJAG_ListCall runs a list-call entry through AS-1 and AS-2
// to the vMCP, for each list method: AS-1 signs it, AS-2 copies it, and the
// vMCP answers the entry's list method with only the entry's server's items,
// on Legacy and Modern, once: a second list call with the token is refused.
// Another list method and a tools/call are refused, and no backend tool runs.
func TestCallBindingIDJAG_ListCall(t *testing.T) {
	t.Parallel()
	c := startIDJAGChain(t)

	// An unbound view, from a tool-call token, holds both backends' tools.
	callToken := c.accessToken(t, chainToolEntry(t, "alpha_echo", `{"input":"x"}`))
	status, body, _ := c.post(t, callToken, c.session(t, callToken), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, nil)
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	require.ElementsMatch(t, []string{"alpha_echo", "beta_echo"}, chainListItems(t, "tools/list", body))

	for method, want := range chainWant {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			token := c.accessToken(t, chainListEntry("alpha", method))
			session := c.session(t, token)
			list := `{"jsonrpc":"2.0","id":2,"method":"` + method + `"}`
			status, body, _ := c.post(t, token, session, list, nil)
			require.Equal(t, http.StatusOK, status, "body: %s", body)
			assert.Equal(t, []string{want}, chainListItems(t, method, body))
			status, body, _ = c.post(t, token, session, list, nil)
			assert.Equal(t, http.StatusUnauthorized, status, "a list call uses the token up: %s", body)
			assert.Contains(t, body, "(token_reused)")

			modernToken := c.accessToken(t, chainListEntry("alpha", method))
			modern := `{"jsonrpc":"2.0","id":3,"method":"` + method + `","params":{"_meta":` +
				`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
			status, body, _ = c.post(t, modernToken, "", modern,
				http.Header{"Mcp-Protocol-Version": {"2026-07-28"}, "Mcp-Method": {method}})
			require.Equal(t, http.StatusOK, status, "body: %s", body)
			assert.Equal(t, []string{want}, chainListItems(t, method, body))

			// A token bound to the other backend lists that backend's item, so
			// both items are there to be told apart.
			betaToken := c.accessToken(t, chainListEntry("beta", method))
			status, body, _ = c.post(t, betaToken, c.session(t, betaToken), list, nil)
			require.Equal(t, http.StatusOK, status, "body: %s", body)
			assert.Equal(t, []string{strings.ReplaceAll(want, "alpha", "beta")}, chainListItems(t, method, body))

			other := "prompts/list"
			if method == other {
				other = "tools/list"
			}
			// Before its list call, a token's other requests are refused as
			// mismatches; after it, as reuse, so the client knows to get a
			// new token.
			fresh := c.accessToken(t, chainListEntry("alpha", method))
			freshSession := c.session(t, fresh)
			for _, tc := range []struct {
				token, session, reason string
				status                 int
			}{
				{fresh, freshSession, "(method_mismatch)", http.StatusForbidden},
				{token, session, "(token_reused)", http.StatusUnauthorized},
			} {
				status, body, _ = c.post(t, tc.token, tc.session, `{"jsonrpc":"2.0","id":4,"method":"`+other+`"}`, nil)
				assert.Equal(t, tc.status, status, "body: %s", body)
				assert.Contains(t, body, tc.reason)
				status, body, _ = c.post(t, tc.token, tc.session, cbToolCall(5, "alpha_echo", `{"input":"x"}`), nil)
				assert.Equal(t, tc.status, status, "body: %s", body)
				assert.Contains(t, body, tc.reason)
			}
			assert.Zero(t, c.toolCalls.Load(), "no list-call token runs a tool")
		})
	}
}

// TestCallBindingIDJAG_ToolCallTokenCannotList runs a tool-call entry
// through the chain: it runs its one call, may list tools unfiltered as
// before, and is refused any other list method.
func TestCallBindingIDJAG_ToolCallTokenCannotList(t *testing.T) {
	t.Parallel()
	c := startIDJAGChain(t)
	token := c.accessToken(t, chainToolEntry(t, "beta_echo", `{"input":"hi"}`))
	session := c.session(t, token)

	status, body, _ := c.post(t, token, session, `{"jsonrpc":"2.0","id":2,"method":"prompts/list"}`, nil)
	assert.Equal(t, http.StatusForbidden, status, "body: %s", body)
	assert.Contains(t, body, "(method_not_allowed)")

	status, body, _ = c.post(t, token, session, cbToolCall(3, "beta_echo", `{"input":"hi"}`), nil)
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Contains(t, body, "hi")
	assert.Equal(t, int32(1), c.toolCalls.Load())
}

// chainToolEntry is a tool-call entry for tool on its backend with args.
func chainToolEntry(t *testing.T, tool, args string) string {
	t.Helper()
	hash, err := callbinding.ArgumentsSHA256(json.RawMessage(args))
	require.NoError(t, err)
	backend, _, _ := strings.Cut(tool, "_")
	return fmt.Sprintf(`{"type":%q,"server":%q,"tool":%q,"arguments_sha256":%q,"operation_id":"op-1"}`,
		toolcalldetails.Type, backend, tool, hash)
}
