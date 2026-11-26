// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpclient "github.com/stacklok/toolhive/pkg/vmcp/client"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/router"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// lcModernBackend is a Modern (2026-07-28) MCP backend that offers the named
// tools, resource templates and prompts. While failPrompts is set, its
// prompts/list fails with HTTP 503, which the vMCP's client takes as a
// transient failure.
type lcModernBackend struct {
	tools       []string
	templates   []string
	prompts     []string
	failPrompts atomic.Bool
}

// start serves the backend and returns its MCP endpoint.
func (b *lcModernBackend) start(tb testing.TB) string {
	tb.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID any `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var result map[string]any
		switch r.Header.Get("Mcp-Method") {
		case "server/discover":
			result = map[string]any{
				"supportedVersions": []string{mcpparser.MCPVersionModern},
				"capabilities":      map[string]any{"tools": map[string]any{}, "resources": map[string]any{}, "prompts": map[string]any{}},
				"serverInfo":        map[string]any{"name": "lc-modern", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": lcItems(b.tools, func(n string) map[string]any {
				return map[string]any{"name": n, "inputSchema": map[string]any{"type": "object"}}
			})}
		case "resources/list":
			result = map[string]any{"resources": []any{}}
		case "resources/templates/list":
			result = map[string]any{"resourceTemplates": lcItems(b.templates, func(u string) map[string]any {
				return map[string]any{"name": u, "uriTemplate": u}
			})}
		case "prompts/list":
			if b.failPrompts.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			result = map[string]any{"prompts": lcItems(b.prompts, func(n string) map[string]any {
				return map[string]any{"name": n}
			})}
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		result["resultType"] = "complete"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	tb.Cleanup(ts.Close)
	return ts.URL
}

func lcItems(names []string, item func(string) map[string]any) []any {
	out := make([]any, 0, len(names))
	for _, n := range names {
		out = append(out, item(n))
	}
	return out
}

// lcRegistry is a backend registry, a gomock mock over backends, whose health
// statuses a test sets.
type lcRegistry struct {
	mu       sync.Mutex
	backends []vmcp.Backend
}

// mock returns a BackendRegistry that answers from r's backends as they are
// when it is called.
func (r *lcRegistry) mock(tb testing.TB) vmcp.BackendRegistry {
	tb.Helper()
	reg := mocks.NewMockBackendRegistry(gomock.NewController(tb))
	reg.EXPECT().List(gomock.Any()).DoAndReturn(func(context.Context) []vmcp.Backend {
		r.mu.Lock()
		defer r.mu.Unlock()
		return slices.Clone(r.backends)
	}).AnyTimes()
	reg.EXPECT().Get(gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, id string) *vmcp.Backend {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i := range r.backends {
			if r.backends[i].ID == id {
				b := r.backends[i]
				return &b
			}
		}
		return nil
	}).AnyTimes()
	reg.EXPECT().Count().DoAndReturn(func() int {
		r.mu.Lock()
		defer r.mu.Unlock()
		return len(r.backends)
	}).AnyTimes()
	return reg
}

func (r *lcRegistry) setStatus(id string, status vmcp.BackendHealthStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.backends {
		if r.backends[i].ID == id {
			r.backends[i].HealthStatus = status
		}
	}
}

// lcAggregator counts the aggregations that reach the backends and, once
// dropAfter is set, leaves that backend out of every aggregation after the
// next one, as a backend that stops answering between two aggregations does.
type lcAggregator struct {
	aggregator.Aggregator
	calls     atomic.Int32
	mu        sync.Mutex
	dropAfter string
	armed     int
}

func (a *lcAggregator) AggregateCapabilities(
	ctx context.Context, backends []vmcp.Backend,
) (*aggregator.AggregatedCapabilities, error) {
	a.calls.Add(1)
	a.mu.Lock()
	drop := ""
	if a.dropAfter != "" {
		a.armed++
		if a.armed > 1 {
			drop = a.dropAfter
		}
	}
	a.mu.Unlock()
	if drop != "" {
		backends = slices.DeleteFunc(slices.Clone(backends), func(b vmcp.Backend) bool { return b.ID == drop })
	}
	return a.Aggregator.AggregateCapabilities(ctx, backends)
}

// dropOnLaterAggregations makes every aggregation after the next one leave
// backend out.
func (a *lcAggregator) dropOnLaterAggregations(backend string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dropAfter, a.armed = backend, 0
}

// lcServer is a vMCP built through server.New over lcModernBackends, whose
// authentication maps a bearer token to the subject and claims registered for
// it. Each token has a subject of its own, so the capability cache never
// answers one list call from another's aggregation.
type lcServer struct {
	url      string
	registry *lcRegistry
	agg      *lcAggregator
	tokens   atomic.Int32
	mu       sync.Mutex
	claims   map[string]map[string]any
}

// newLCServer builds the vMCP over backends (ID to backend), all healthy, with
// the Cedar policies given (none leaves authorization unconfigured).
func newLCServer(tb testing.TB, backends map[string]*lcModernBackend, policies ...string) *lcServer {
	tb.Helper()
	s := &lcServer{registry: &lcRegistry{}, claims: map[string]map[string]any{}}
	ids := make([]string, 0, len(backends))
	for id := range backends {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s.registry.backends = append(s.registry.backends, vmcp.Backend{
			ID: id, Name: id, BaseURL: backends[id].start(tb), TransportType: "streamable-http",
			HealthStatus: vmcp.BackendHealthy,
		})
	}

	authReg := vmcpauth.NewDefaultOutgoingAuthRegistry()
	require.NoError(tb, authReg.RegisterStrategy(authtypes.StrategyTypeUnauthenticated, strategies.NewUnauthenticatedStrategy()))
	backendClient, err := vmcpclient.NewHTTPBackendClient(authReg)
	require.NoError(tb, err)
	// A priority resolver keeps raw names, so Cedar policies can name them.
	resolver, err := aggregator.NewPriorityConflictResolver(ids)
	require.NoError(tb, err)
	s.agg = &lcAggregator{Aggregator: aggregator.NewDefaultAggregator(backendClient, resolver, nil, nil)}

	var authzCfg *authorizers.Config
	if len(policies) > 0 {
		authzCfg, err = authorizers.NewConfig(cedar.Config{
			Version: "1.0", Type: cedar.ConfigType,
			Options: &cedar.ConfigOptions{Policies: policies, EntitiesJSON: "[]"},
		})
		require.NoError(tb, err)
	}
	authMw := func(next http.Handler) http.Handler {
		withParser := mcpparser.ParsingMiddleware(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			s.mu.Lock()
			claims, ok := s.claims[token]
			s.mu.Unlock()
			if !ok {
				http.Error(w, "unknown token", http.StatusUnauthorized)
				return
			}
			subject, _ := claims["sub"].(string)
			identity := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: subject, Claims: claims}}
			withParser.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), identity)))
		})
	}
	srv, err := server.New(tb.Context(), &server.Config{
		Name: "lc-vmcp", Host: "127.0.0.1", Port: 0, SessionTTL: 5 * time.Minute,
		SessionFactory: vmcpsession.NewSessionFactory(authReg), Aggregator: s.agg,
		AuthMiddleware: authMw, Authz: authzCfg,
	}, router.NewSessionRouter(&vmcp.RoutingTable{}), backendClient, s.registry.mock(tb), nil)
	require.NoError(tb, err)
	handler, err := srv.Handler(tb.Context())
	require.NoError(tb, err)
	ts := httptest.NewServer(handler)
	tb.Cleanup(ts.Close)
	s.url = ts.URL
	return s
}

// listToken registers a token, with a subject of its own, bound to listing
// method on backend, and returns it.
func (s *lcServer) listToken(backend, method string) string {
	n := s.tokens.Add(1)
	token := fmt.Sprintf("lc-token-%d", n)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[token] = map[string]any{
		"iss": "https://issuer.example.com", "sub": fmt.Sprintf("lc-user-%d", n), "jti": token,
		"exp": float64(time.Now().Add(time.Hour).Unix()),
		"authorization_details": []any{map[string]any{
			"type": toolcalldetails.Type, "server": backend, "method": method,
		}},
	}
	return token
}

// list sends a Modern list request for method as the holder of token, and
// returns the status and body.
func (s *lcServer) list(tb testing.TB, token, method string) (int, string) {
	tb.Helper()
	body := `{"jsonrpc":"2.0","id":2,"method":"` + method + `","params":{"_meta":` +
		`{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/mcp", strings.NewReader(body))
	require.NoError(tb, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(tb, err)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	require.NoError(tb, err)
	return resp.StatusCode, string(raw)
}

// TestCallBindingReal_ListCallOneAggregation checks, against the real core,
// that a list call's server check and items come from one aggregation. The
// backend drops out of every aggregation after the first. With the check and
// the items taken from separate aggregations, the call passed the check and
// was answered with an empty list, spending the token; from one aggregation
// it is answered with the backend's tools. An unhealthy third backend makes
// the registry differ from the backends the lists include, as it does
// whenever a backend is unhealthy, so no aggregation is answered from the
// capability cache.
func TestCallBindingReal_ListCallOneAggregation(t *testing.T) {
	t.Parallel()
	s := newLCServer(t, map[string]*lcModernBackend{
		"alpha": {tools: []string{"alpha_tool"}},
		"beta":  {tools: []string{"beta_tool"}},
		"gamma": {tools: []string{"gamma_tool"}},
	})
	s.registry.setStatus("gamma", vmcp.BackendUnhealthy)

	s.agg.dropOnLaterAggregations("beta")
	before := s.agg.calls.Load()
	status, body := s.list(t, s.listToken("beta", "tools/list"), "tools/list")
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Equal(t, []string{"beta_tool"}, chainListItems(t, "tools/list", body))
	assert.Equal(t, int32(1), s.agg.calls.Load()-before, "one aggregation answers the list call")
}

// TestCallBindingReal_ListCallTransientList checks, against the real core,
// that a list call whose method's list failed transiently on the bound
// backend gets 503 and keeps the token, rather than being answered with the
// empty list the failure was reported as. The backend's other lists are
// still answered. A backend the caller could see only through the list that
// failed is not known to be in its view, so it gets 403 server_not_found, as
// an unknown server does: a 503 would tell that it exists.
func TestCallBindingReal_ListCallTransientList(t *testing.T) {
	t.Parallel()
	backend := &lcModernBackend{tools: []string{"alpha_tool"}, prompts: []string{"alpha_prompt"}}
	backend.failPrompts.Store(true)
	promptsOnly := &lcModernBackend{prompts: []string{"beta_prompt"}}
	promptsOnly.failPrompts.Store(true)
	s := newLCServer(t, map[string]*lcModernBackend{"alpha": backend, "beta": promptsOnly})

	unknownStatus, unknownBody := s.list(t, s.listToken("no-such-backend", "prompts/list"), "prompts/list")
	status, body := s.list(t, s.listToken("beta", "prompts/list"), "prompts/list")
	assert.Equal(t, http.StatusForbidden, status, "body: %s", body)
	assert.Equal(t, unknownStatus, status)
	assert.Equal(t, unknownBody, body)

	token := s.listToken("alpha", "prompts/list")
	for range 2 {
		status, body := s.list(t, token, "prompts/list")
		require.Equal(t, http.StatusServiceUnavailable, status, "the token stays unused: %s", body)
		assert.NotContains(t, body, "token_reused")
	}

	status, body = s.list(t, s.listToken("alpha", "tools/list"), "tools/list")
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Equal(t, []string{"alpha_tool"}, chainListItems(t, "tools/list", body))
}

// TestCallBindingReal_ListCallTemplatesOnlyUnhealthy checks, against the real
// core, that a backend in the caller's view only through its resource
// templates, reachable but marked unhealthy, gets 503 and keeps the token, as
// any other backend in the view does; and that the same token lists its
// templates once it is healthy again.
func TestCallBindingReal_ListCallTemplatesOnlyUnhealthy(t *testing.T) {
	t.Parallel()
	s := newLCServer(t, map[string]*lcModernBackend{
		"alpha": {tools: []string{"alpha_tool"}},
		"tmpl":  {templates: []string{"file:///docs/{path}"}},
	})
	s.registry.setStatus("tmpl", vmcp.BackendUnhealthy)

	token := s.listToken("tmpl", "resources/templates/list")
	status, body := s.list(t, token, "resources/templates/list")
	require.Equal(t, http.StatusServiceUnavailable, status, "body: %s", body)
	assert.NotContains(t, body, "server_not_found")

	s.registry.setStatus("tmpl", vmcp.BackendHealthy)
	status, body = s.list(t, token, "resources/templates/list")
	require.Equal(t, http.StatusOK, status, "body: %s", body)
	assert.Equal(t, []string{"file:///docs/{path}"}, chainListItems(t, "resources/templates/list", body))
}

// TestCallBindingReal_ListCallHiddenUnhealthy checks, against the real core,
// that an unhealthy backend the caller is admitted to nothing on gets the
// same 403 server_not_found as an unknown server, not the 503 that would tell
// it exists. "shadow" offers only a tool named as healthy alpha's, and "zeta"
// only a tool named as unhealthy beta's; the priority strategy (backends in
// name order) drops each from it whenever the whole registry is aggregated,
// as ListBackends aggregates it. Aggregated alone, or with only the healthy
// backends, their tools would be kept and admitted.
func TestCallBindingReal_ListCallHiddenUnhealthy(t *testing.T) {
	t.Parallel()
	s := newLCServer(t, map[string]*lcModernBackend{
		"alpha":  {tools: []string{"alpha_tool"}},
		"beta":   {tools: []string{"shared_tool"}},
		"hidden": {tools: []string{"hidden_tool"}},
		"shadow": {tools: []string{"alpha_tool"}},
		"zeta":   {tools: []string{"shared_tool"}},
	}, `permit(principal, action == Action::"call_tool", resource == Tool::"alpha_tool");`,
		`permit(principal, action == Action::"call_tool", resource == Tool::"shared_tool");`)
	for _, id := range []string{"beta", "hidden", "shadow", "zeta"} {
		s.registry.setStatus(id, vmcp.BackendUnhealthy)
	}

	unknownStatus, unknownBody := s.list(t, s.listToken("no-such-backend", "tools/list"), "tools/list")
	assert.Equal(t, http.StatusForbidden, unknownStatus, "body: %s", unknownBody)
	assert.Contains(t, unknownBody, "(server_not_found)")
	for _, backend := range []string{"hidden", "shadow", "zeta"} {
		status, body := s.list(t, s.listToken(backend, "tools/list"), "tools/list")
		assert.Equal(t, unknownStatus, status, "%s: %s", backend, body)
		assert.Equal(t, unknownBody, body, backend)
	}
}

// BenchmarkCallBindingReal_ListCallCedar times, with Cedar on, a list call on a
// backend of 9,000 tools next to one of 1,000, against a plain tools/list of
// the same 10,000 tools. Every request has a subject of its own, so each is
// aggregated afresh, as the first request of a caller is.
func BenchmarkCallBindingReal_ListCallCedar(b *testing.B) {
	names := func(prefix string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%s_%05d", prefix, i)
		}
		return out
	}
	s := newLCServer(b, map[string]*lcModernBackend{
		"alpha": {tools: names("alpha", 9000)},
		"beta":  {tools: names("beta", 1000)},
	}, `permit(principal, action == Action::"call_tool", resource);`)

	b.Run("plain tools/list", func(b *testing.B) {
		for b.Loop() {
			token := s.listToken("alpha", "tools/list")
			s.mu.Lock()
			delete(s.claims[token], "authorization_details")
			s.mu.Unlock()
			status, body := s.list(b, token, "tools/list")
			require.Equal(b, http.StatusOK, status, "body: %.200s", body)
		}
	})
	b.Run("list call", func(b *testing.B) {
		for b.Loop() {
			status, body := s.list(b, s.listToken("alpha", "tools/list"), "tools/list")
			require.Equal(b, http.StatusOK, status, "body: %.200s", body)
		}
	})
}
