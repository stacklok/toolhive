// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/upstreamtoken"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/identity"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	aggmocks "github.com/stacklok/toolhive/pkg/vmcp/aggregator/mocks"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	authfactory "github.com/stacklok/toolhive/pkg/vmcp/auth/factory"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

const (
	credentialIssuer   = "https://embedded.example"
	credentialAudience = "https://vmcp.example/mcp"
	credentialUser     = "authenticated-user"
	credentialOther    = "other-user"
	credentialProvider = "provider-a"
	credentialToken    = "user-upstream-a"
)

func TestDelegatedCredentialRead(t *testing.T) {
	t.Parallel()
	for _, version := range []string{mcpparser.MCPVersionLegacy, mcpparser.MCPVersionModern} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			for _, failure := range []string{"needs authorization", "wrong owner", "no canonical user"} {
				t.Run(failure, func(t *testing.T) {
					t.Parallel()
					testDelegatedCredentialCall(t, version, failure)
				})
			}
		})
	}
}

func testDelegatedCredentialCall(t *testing.T, version, failure string) {
	t.Helper()
	as, raw := newCredentialAuthServer(t, credentialIssuer, runner.WithUserKeyedUpstreamStorage())
	stor := as.IDPTokenStorage()
	reader := upstreamtoken.NewInProcessService(stor, as.UpstreamTokenRefresher())
	seedCredential(t, stor, credentialUser, credentialProvider, credentialToken)
	seedCredential(t, stor, credentialOther, credentialProvider, "other-user-upstream")
	seedCredential(t, stor, credentialUser, "provider-b", "user-upstream-b")
	backendA := newCredentialBackend(t, credentialProvider, "tool-a")
	backendB := newCredentialBackend(t, "provider-b", "tool-b")

	outgoingReader := reader
	var missingCanonicalUser atomic.Bool
	if failure == "no canonical user" {
		// For a valid PlatformUserID, the real reader supplies the canonical user.
		// Inject the storage-side failure instead of changing the admitted identity.
		fault := &canonicalUserFault{UpstreamTokenStorage: stor, provider: credentialProvider, on: &missingCanonicalUser}
		outgoingReader = upstreamtoken.NewInProcessService(fault, as.UpstreamTokenRefresher())
	}
	incoming := credentialIncoming(t, as, reader, credentialIssuer)
	strategy := strategies.NewUpstreamInjectStrategy(strategies.WithUserCredentialReader(outgoingReader))
	handler := newCredentialVMCP(t, incoming, strategy, nil, backendA.backend, backendB.backend)
	rpc := credentialRPC{handler: handler, token: signCredentialToken(t, as, credentialIssuer, delegatedClaims()), version: version}
	// Discover while both credentials are live so the later call still routes to
	// provider A even after its credential becomes unusable.
	rpc.warm(t, "tool-a", "tool-b")

	before := len(backendA.headers())
	// The live row must win over both user identifiers supplied by the caller.
	rpc.requireSuccess(t, "tool-a", map[string]any{"user_id": credentialOther, "sub": credentialOther})
	assertCredentialHeaders(t, backendA.headers()[before:], credentialToken)
	assertCredentialHeaders(t, backendA.headers(), credentialToken)

	switch failure {
	case "needs authorization":
		// Model an expired access token that can no longer be refreshed.
		ctx := identity.WithCanonicalUser(t.Context(), credentialUser)
		require.NoError(t, stor.StoreUpstreamTokens(ctx, "", credentialProvider, &storage.UpstreamTokens{
			UserID: credentialUser, AccessToken: credentialToken, ExpiresAt: time.Now().Add(-time.Hour),
		}))
	case "wrong owner":
		// Model a corrupt row: the user-keyed writer correctly refuses this owner mismatch.
		require.NoError(t, raw.StoreUpstreamTokens(t.Context(), "user:"+credentialUser, credentialProvider,
			&storage.UpstreamTokens{UserID: credentialOther, AccessToken: "other-user-upstream"}))
	case "no canonical user":
		missingCanonicalUser.Store(true)
	}

	before = len(backendA.headers())
	response := rpc.call(t, "tool-a", nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.Empty(t, response.Header().Get("WWW-Authenticate"))
	assert.NotContains(t, response.Body.String(), backendA.backend.BaseURL)
	assert.NotContains(t, response.Body.String(), backendB.backend.BaseURL)
	assert.Equal(t, before, len(backendA.headers()), "failed credential lookup must not reach the backend")
	if failure == "needs authorization" {
		result := toolCredentialResult(t, response)
		assert.True(t, result.IsError)
		assert.Equal(t, []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("provider %q requires upstream authorization", credentialProvider),
		}}, result.Content)

		beforeB := len(backendB.headers())
		rpc.requireSuccess(t, "tool-b", nil)
		assertCredentialHeaders(t, backendB.headers()[beforeB:], "user-upstream-b")
		assert.Equal(t, before, len(backendA.headers()), "provider B must not dial provider A")
		return
	}
	assertCredentialUnavailable(t, response)
	assert.NotContains(t, response.Body.String(), credentialProvider)
	assert.NotContains(t, response.Body.String(), "private storage detail")
	assert.NotContains(t, response.Body.String(), storage.ErrInvalidBinding.Error())
	assert.NotContains(t, response.Body.String(), storage.ErrNoCanonicalUser.Error())
}

func TestDelegatedCredentialNotDiscoveredBeforeConnection(t *testing.T) {
	t.Parallel()
	for _, version := range []string{mcpparser.MCPVersionLegacy, mcpparser.MCPVersionModern} {
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			as, _ := newCredentialAuthServer(t, credentialIssuer, runner.WithUserKeyedUpstreamStorage())
			stor := as.IDPTokenStorage()
			reader := upstreamtoken.NewInProcessService(stor, as.UpstreamTokenRefresher())
			// Keep one backend discoverable so discovery returns a partial view, not a sweep error.
			seedCredential(t, stor, credentialUser, "provider-b", "user-upstream-b")
			backendA := newCredentialBackend(t, credentialProvider, "tool-a")
			backendB := newCredentialBackend(t, "provider-b", "tool-b")
			incoming := credentialIncoming(t, as, reader, credentialIssuer)
			strategy := strategies.NewUpstreamInjectStrategy(strategies.WithUserCredentialReader(reader))
			handler := newCredentialVMCP(t, incoming, strategy, nil, backendA.backend, backendB.backend)
			rpc := credentialRPC{handler: handler, token: signCredentialToken(t, as, credentialIssuer, delegatedClaims()), version: version}
			rpc.warm(t, backendB.tool)
			assertCredentialToolHidden(t, rpc, backendA.tool, backendB.tool)

			// Connecting does not update the existing capability view immediately.
			seedCredential(t, stor, credentialUser, credentialProvider, credentialToken)
			assertCredentialToolHidden(t, rpc, backendA.tool, backendB.tool)
			assert.Empty(t, backendA.headers())
		})
	}
}

func assertCredentialToolHidden(t *testing.T, rpc credentialRPC, hidden, present string) {
	t.Helper()
	list := rpc.post(t, "tools/list", "", nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	var envelope struct {
		Result *struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal(list.Body.Bytes(), &envelope), list.Body.String())
	require.Empty(t, envelope.Error, list.Body.String())
	require.NotNil(t, envelope.Result, list.Body.String())
	names := make([]string, 0, len(envelope.Result.Tools))
	for _, tool := range envelope.Result.Tools {
		names = append(names, tool.Name)
	}
	assert.NotContains(t, names, hidden)
	assert.Contains(t, names, present)

	message := "unknown tool"
	if rpc.version == mcpparser.MCPVersionLegacy {
		message = fmt.Sprintf("tool %q not found", hidden)
	}
	assertCredentialRPCError(t, rpc.call(t, hidden, nil), -32602, message)
}

func TestDelegatedCredentialWithoutReader(t *testing.T) {
	t.Parallel()
	as, _ := newCredentialAuthServer(t, credentialIssuer)
	backend := newCredentialBackend(t, credentialProvider, "tool-a")
	incoming := credentialIncoming(t, as, nil, credentialIssuer)
	withSessionTokens := func(next http.Handler) http.Handler {
		return incoming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, _ := auth.IdentityFromContext(r.Context())
			require.NotNil(t, id)
			require.Equal(t, auth.AdmissionRouteDelegated, id.AdmissionRoute)
			copyID := *id
			copyID.UpstreamTokens = map[string]string{credentialProvider: "must-not-be-injected"}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), &copyID)))
		}))
	}
	// Supply an already advertised tool: real discovery without a reader would drop
	// it, and tools/call would never reach the outbound no-reader guard.
	target := vmcp.BackendToTarget(&backend.backend)
	target.OriginalCapabilityName = backend.tool
	view := aggmocks.NewMockAggregator(gomock.NewController(t))
	view.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		Tools:        []vmcp.Tool{{Name: backend.tool}},
		RoutingTable: &vmcp.RoutingTable{Tools: map[string]*vmcp.BackendTarget{backend.tool: target}},
	}, nil).AnyTimes()
	handler := newCredentialVMCP(t, withSessionTokens, strategies.NewUpstreamInjectStrategy(), view, backend.backend)
	rpc := credentialRPC{handler: handler, token: signCredentialToken(t, as, credentialIssuer, delegatedClaims()), version: mcpparser.MCPVersionModern}
	rpc.warm(t, backend.tool)
	assertCredentialUnavailable(t, rpc.call(t, backend.tool, nil))
	assert.Empty(t, backend.headers(), "no HTTP requests, even with upstream tokens on the identity")
}

func TestDelegatedCredentialRouteNegatives(t *testing.T) {
	t.Parallel()
	for _, foreign := range []bool{false, true} {
		name := "sessionless exchange"
		if foreign {
			name = "foreign delegated marker"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			as, _ := newCredentialAuthServer(t, credentialIssuer, runner.WithUserKeyedUpstreamStorage())
			seedCredential(t, as.IDPTokenStorage(), credentialUser, credentialProvider, credentialToken)
			var reader upstreamtoken.TokenReader = upstreamtoken.NewInProcessService(as.IDPTokenStorage(), as.UpstreamTokenRefresher())
			issuer, signer := credentialIssuer, as
			claims := map[string]any{
				"may_act": map[string]any{"sub": "agent"}, claimnames.NoUpstreamSessionKey: true,
			}
			if foreign {
				issuer = "https://foreign.example"
				signer, _ = newCredentialAuthServer(t, issuer)
				claims = delegatedClaims()
				// Trust the foreign issuer for authentication, not for access to the embedded user-keyed store.
				reader = nil
			}
			incoming := credentialIncoming(t, signer, reader, issuer)
			observed := func(next http.Handler) http.Handler {
				return incoming(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id, _ := auth.IdentityFromContext(r.Context())
					require.NotNil(t, id)
					assert.Equal(t, auth.AdmissionRouteUnspecified, id.AdmissionRoute)
					assert.Empty(t, id.UpstreamTokens)
					if !foreign {
						assert.True(t, id.SessionlessGrant)
					}
					next.ServeHTTP(w, r)
				}))
			}
			// No GetUpstreamTokens expectation: any per-user reader invocation fails the test.
			noReads := storagemocks.NewMockUpstreamTokenStorage(gomock.NewController(t))
			strategy := strategies.NewUpstreamInjectStrategy(strategies.WithUserCredentialReader(
				upstreamtoken.NewInProcessService(noReads, nil)))
			backend := newCredentialBackend(t, credentialProvider, "tool-a")
			handler := newCredentialVMCP(t, observed, strategy, nil, backend.backend)
			rpc := credentialRPC{handler: handler, token: signCredentialToken(t, signer, issuer, claims), version: mcpparser.MCPVersionModern}
			response := rpc.post(t, "tools/list", "", nil)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			// The only backend cannot be enumerated without an upstream token.
			assertCredentialRPCError(t, response, -32603, "internal error")
			assert.Empty(t, backend.headers())
		})
	}
}

func newCredentialAuthServer(t *testing.T, issuer string, opts ...runner.EmbeddedAuthServerOption) (*runner.EmbeddedAuthServer, *storage.MemoryStorage) {
	t.Helper()
	raw := storage.NewMemoryStorage()
	t.Cleanup(func() { assert.NoError(t, raw.Close()) })
	cfg := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion, Issuer: issuer, AllowedAudiences: []string{credentialAudience},
		Upstreams: []authserver.UpstreamRunConfig{{
			Name: credentialProvider, Type: authserver.UpstreamProviderTypeOAuth2,
			OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
				AuthorizationEndpoint: "https://upstream.example/authorize", TokenEndpoint: "https://upstream.example/token",
				ClientID: "login-client", RedirectURI: issuer + "/oauth/callback",
			},
		}},
	}
	as, err := runner.NewEmbeddedAuthServerWithStorage(t.Context(), cfg, raw, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, as.Close()) })
	return as, raw
}

func credentialIncoming(t *testing.T, signer *runner.EmbeddedAuthServer, reader upstreamtoken.TokenReader, issuer string) func(http.Handler) http.Handler {
	t.Helper()
	incoming, _, _, err := authfactory.NewIncomingAuthMiddleware(t.Context(), &config.IncomingAuthConfig{
		Type: config.IncomingAuthTypeOIDC,
		OIDC: &config.OIDCConfig{Issuer: issuer, Audience: credentialAudience, Resource: credentialAudience, JWKSURL: issuer + "/.well-known/jwks.json"},
	}, "credential-test", nil, reader, signer.KeyProvider(), credentialIssuer)
	require.NoError(t, err)
	return incoming
}

func delegatedClaims() map[string]any {
	return map[string]any{
		claimnames.DelegatedAccessKey: true,
		"act":                         map[string]any{"iss": "https://agents.example", "sub": "agent"},
	}
}

func signCredentialToken(t *testing.T, as *runner.EmbeddedAuthServer, issuer string, claims map[string]any) string {
	t.Helper()
	key, err := as.KeyProvider().SigningKey(t.Context())
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.SignatureAlgorithm(key.Algorithm), Key: key.Key},
		(&jose.SignerOptions{}).WithType("at+jwt").WithHeader("kid", key.KeyID))
	require.NoError(t, err)
	now := time.Now()
	claims["iss"], claims["sub"], claims["aud"] = issuer, credentialUser, []string{credentialAudience}
	claims["iat"], claims["exp"], claims["client_id"] = now.Unix(), now.Add(time.Hour).Unix(), "agent-client"
	payload, err := json.Marshal(claims)
	require.NoError(t, err)
	signed, err := signer.Sign(payload)
	require.NoError(t, err)
	token, err := signed.CompactSerialize()
	require.NoError(t, err)
	return token
}

func seedCredential(t *testing.T, stor storage.UpstreamTokenStorage, user, provider, token string) {
	t.Helper()
	ctx := identity.WithCanonicalUser(t.Context(), user)
	require.NoError(t, stor.StoreUpstreamTokens(ctx, "", provider, &storage.UpstreamTokens{
		UserID: user, AccessToken: token, ExpiresAt: time.Now().Add(time.Hour),
	}))
}

type credentialBackend struct {
	backend vmcp.Backend
	tool    string
	mu      sync.Mutex
	authz   [][]string
}

func newCredentialBackend(t *testing.T, provider, tool string) *credentialBackend {
	t.Helper()
	b := &credentialBackend{tool: tool}
	sdk := mcp.NewServer(&mcp.Implementation{Name: provider, Version: "1.0.0"}, nil)
	sdk.AddTool(&mcp.Tool{Name: tool, InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "called " + tool}}}, nil
		})
	stream := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return sdk },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.authz = append(b.authz, r.Header.Values("Authorization"))
		b.mu.Unlock()
		stream.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})
	b.backend = vmcp.Backend{ID: provider, Name: provider, TransportType: "streamable-http", BaseURL: ts.URL + "/mcp",
		AuthConfig: &authtypes.BackendAuthStrategy{Type: authtypes.StrategyTypeUpstreamInject,
			UpstreamInject: &authtypes.UpstreamInjectConfig{ProviderName: provider}},
	}
	return b
}

func (b *credentialBackend) headers() [][]string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([][]string(nil), b.authz...)
}

func assertCredentialHeaders(t *testing.T, headers [][]string, token string) {
	t.Helper()
	require.NotEmpty(t, headers, "expected an authenticated backend request")
	for _, values := range headers {
		assert.Equal(t, []string{"Bearer " + token}, values)
	}
}

func newCredentialVMCP(t *testing.T, incoming func(http.Handler) http.Handler, strategy vmcpauth.Strategy,
	view aggregator.Aggregator, backends ...vmcp.Backend,
) http.Handler {
	t.Helper()
	registry := mocks.NewMockBackendRegistry(gomock.NewController(t))
	registry.EXPECT().List(gomock.Any()).Return(backends).AnyTimes()
	names := make([]string, 0, len(backends))
	for _, backend := range backends {
		registry.EXPECT().Get(gomock.Any(), backend.ID).Return(&backend).AnyTimes()
		names = append(names, backend.Name)
	}
	outgoing := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(t, outgoing.RegisterStrategy(strategy.Name(), strategy))
	client, err := vmcpclient.NewHTTPBackendClient(outgoing)
	require.NoError(t, err)
	if view == nil {
		resolver, err := aggregator.NewPriorityConflictResolver(names)
		require.NoError(t, err)
		view = aggregator.NewDefaultAggregator(client, resolver, nil, nil)
	}
	srv, err := server.New(t.Context(), &server.Config{
		Name: "credential-test", Host: "127.0.0.1", SessionTTL: time.Minute,
		SessionFactory: vmcpsession.NewSessionFactory(outgoing), Aggregator: view, AuthMiddleware: incoming,
	}, router.NewSessionRouter(&vmcp.RoutingTable{}), client, registry, nil)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, srv.Stop(context.Background())) })
	handler, err := srv.Handler(t.Context())
	require.NoError(t, err)
	return handler
}

// credentialRequestID gives every request a fresh ID; MCP forbids reusing one within a session.
var credentialRequestID atomic.Int64

type credentialRPC struct {
	handler http.Handler
	token   string
	version string
	session string
}

func (c *credentialRPC) warm(t *testing.T, tools ...string) {
	t.Helper()
	if c.version != mcpparser.MCPVersionModern {
		response := c.post(t, "initialize", "", map[string]any{
			"protocolVersion": c.version, "clientInfo": map[string]string{"name": "test", "version": "1"},
			"capabilities": map[string]any{},
		})
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		c.session = response.Header().Get("Mcp-Session-Id")
		require.NotEmpty(t, c.session)
		response = c.post(t, "notifications/initialized", "", nil)
		require.Equal(t, http.StatusAccepted, response.Code, response.Body.String())
	}
	response := c.post(t, "tools/list", "", nil)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	for _, tool := range tools {
		require.Contains(t, response.Body.String(), fmt.Sprintf(`"name":%q`, tool), "discovery must advertise the tool")
	}
}

func (c credentialRPC) call(t *testing.T, tool string, args map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	params := map[string]any{"name": tool}
	if args != nil {
		params["arguments"] = args
	}
	return c.post(t, "tools/call", tool, params)
}

func (c credentialRPC) requireSuccess(t *testing.T, tool string, args map[string]any) {
	t.Helper()
	response := c.call(t, tool, args)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assert.False(t, toolCredentialResult(t, response).IsError, response.Body.String())
	assert.Contains(t, response.Body.String(), "called "+tool)
}

func (c credentialRPC) post(t *testing.T, method, name string, params map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if params == nil {
		params = map[string]any{}
	}
	if c.version == mcpparser.MCPVersionModern {
		params["_meta"] = map[string]any{
			"io.modelcontextprotocol/protocolVersion": c.version, "io.modelcontextprotocol/clientCapabilities": map[string]any{},
		}
	}
	envelope := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if method != "notifications/initialized" {
		envelope["id"] = credentialRequestID.Add(1)
	}
	payload, err := json.Marshal(envelope)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", c.version)
	if c.version == mcpparser.MCPVersionModern {
		req.Header.Set("Mcp-Method", method)
		if name != "" {
			req.Header.Set("Mcp-Name", name)
		}
	} else if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, req)
	return response
}

func toolCredentialResult(t *testing.T, response *httptest.ResponseRecorder) *mcp.CallToolResult {
	t.Helper()
	var envelope struct {
		Result *mcp.CallToolResult `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.NotNil(t, envelope.Result, response.Body.String())
	return envelope.Result
}

func assertCredentialUnavailable(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	assertCredentialRPCError(t, response, -32603, "upstream credential unavailable")
}

func assertCredentialRPCError(t *testing.T, response *httptest.ResponseRecorder, code int, message string) {
	t.Helper()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope), response.Body.String())
	require.NotNil(t, envelope.Error, response.Body.String())
	assert.Empty(t, envelope.Result, response.Body.String())
	assert.Equal(t, code, envelope.Error.Code)
	assert.Equal(t, message, envelope.Error.Message)
}

// canonicalUserFault decorates the user-keyed storage, so the reader built on
// it still reports UserKeyed, and fails reads for one provider on demand.
type canonicalUserFault struct {
	storage.UpstreamTokenStorage
	provider string
	on       *atomic.Bool
}

func (f *canonicalUserFault) Unwrap() storage.UpstreamTokenStorage { return f.UpstreamTokenStorage }

func (f *canonicalUserFault) GetUpstreamTokens(
	ctx context.Context, sid, provider string,
) (*storage.UpstreamTokens, error) {
	if provider == f.provider && f.on.Load() {
		return nil, fmt.Errorf("private storage detail: %w", storage.ErrNoCanonicalUser)
	}
	return f.UpstreamTokenStorage.GetUpstreamTokens(ctx, sid, provider)
}
