// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

// callBindingLeeway is the clock skew allowed on a call-bound token's exp. It
// is the token validator's leeway: pkg/auth's TokenValidator refuses a JWT
// once its exp has passed, with no leeway, so this is zero. Keep the two
// equal. The middleware checks exp itself as well, so a token is never
// accepted after its record could have expired, whatever validator the
// embedder installs.
const callBindingLeeway = 0 * time.Second

// callBindingRetentionMargin is how long a token's record outlives the last
// moment the vMCP accepts the token. It covers a record kept in a shared
// store whose clock runs ahead of this replica's, which would otherwise
// forget a key while this replica still accepts the token.
const callBindingRetentionMargin = time.Minute

// Methods a call-bound token may send without starting a call: the Legacy
// handshake's notification, the Modern capability probe and ping. initialize
// is allowed only until the token starts its call (serveInitialize); tools/list
// with a tool-call entry, and the entry's list method with a list-call entry
// (servePost).
var callBindingPassMethods = []string{
	"notifications/initialized", "ping", methodServerDiscover,
}

// callBindingTokenReasons are the refusals that describe the token rather
// than the request: no request the client could send with this token would
// be accepted, so the client needs a new one. They get HTTP 401 with an
// invalid_token challenge (RFC 6750 §3.1). Every other refusal is about the
// request and gets HTTP 403 without a challenge, as the CallGate and pkg/authz
// answer a denial: an insufficient_scope challenge is what MCP clients read as
// a step-up hint, and no scope fixes these refusals.
var callBindingTokenReasons = []string{
	callbinding.ReasonTokenExpired,
	callbinding.ReasonTokenReused,
	callbinding.ReasonMalformedEntry,
	callbinding.ReasonSingleEntry,
	callbinding.ReasonClaimsRequired,
}

// callBindingRoute resolves a tools/call name to the backend it is routed to
// for identity, or to "" when the tool has no single backend.
type callBindingRoute func(ctx context.Context, identity *auth.Identity, name string) (string, error)

// callBindingListItems returns, rendered as list result items, the items the
// list method lists for identity from the backend named server. It returns
// errListServerNotFound when server is not a backend in identity's view, and
// any other error when the items cannot be listed now.
type callBindingListItems func(
	ctx context.Context, identity *auth.Identity, method, server string,
) ([]json.RawMessage, error)

// errListServerNotFound reports a list call on a server that is not a backend
// in the caller's view.
var errListServerNotFound = errors.New("call binding: the list call's server is not a backend in the caller's view")

// callBinding enforces access tokens that carry an RFC 9396
// authorization_details entry of type https://toolhive.dev/mcp_tool_call (see
// pkg/mcp/toolcalldetails). A token with a tool-call entry may start one
// tools/call, the one its entry names, and only once. A token with a list-call
// entry may send the list method its entry names, answered with only its
// server's items.
type callBinding struct {
	// binding is the configuration, record and returned calls this check
	// shares with the token validator (see callbinding.Binding).
	binding   *callbinding.Binding
	route     callBindingRoute
	listItems callBindingListItems
	now       func() time.Time

	// noClientWarning logs once that Clients is set and a token names no
	// client.
	noClientWarning sync.Once
	// unwiredWarning logs once that a call-bound tool call started while the
	// token validator was never given the binding's option.
	unwiredWarning sync.Once
}

// newCallBinding builds the enforcement binding describes. A nil binding is
// the zero Config's.
func newCallBinding(
	binding *callbinding.Binding, route callBindingRoute, listItems callBindingListItems,
) *callBinding {
	if binding == nil {
		// A zero Config cannot fail validation.
		binding, _ = callbinding.New(callbinding.Config{})
	}
	return &callBinding{
		binding:   binding,
		route:     route,
		listItems: listItems,
		now:       time.Now,
	}
}

// callBindingRoute resolves name the way this instance dispatches a
// tools/call. With the optimizer enabled, a Legacy session's only tools are
// the optimizer's meta-tools (serve_optimizer.go) and Modern is refused
// (modern_gate.go), so no name reaches a single backend. Otherwise the core
// routes every tools/call, on both revisions, and a tool's BackendID names its
// backend; composite tools and code mode's execute_tool_script have none.
// The decision follows what a name resolves to, so a new kind of tool that
// dispatches further calls is refused as long as it has no BackendID. The core
// checks the backend again on the view it dispatches from (core.CallTool).
func (s *Server) callBindingRoute(ctx context.Context, identity *auth.Identity, name string) (string, error) {
	if s.optimizerFactory != nil {
		return "", nil
	}
	tool, err := s.core.LookupTool(ctx, identity, name)
	if err != nil {
		return "", err
	}
	return tool.BackendID, nil
}

// callBindingListItems answers a list-call token from one aggregation: the
// core's view of server (core.BackendCapabilitiesLookup), which decides whether
// server is in identity's view and gives the items identity is admitted to there,
// rendered as a Modern list renders them, which is how Legacy sessions advertise
// them too. Each item carries its own BackendID from that one view, so another
// backend's item that shares a name or URI is never answered, and the server
// cannot drop out of the view between the check and the items. A composite
// tool, code mode's execute_tool_script and, with the optimizer enabled, the
// find_tool and call_tool meta-tools have no BackendID, so they are never
// listed: with the optimizer, no tool is. A core that does not implement the
// lookup, such as a decorator that only embeds core.VMCP, is answered from its
// own lists instead (callBindingListsCapabilities).
//
// It returns errListServerNotFound, which the caller answers with 403
// server_not_found, when server is unknown, hidden from identity by
// authorization, offers identity nothing, or contributes nothing to the view
// (its discovery fails), as checkToolCall answers tool_not_found: AS-1 signs
// any server name, so the answer must not tell whether a backend the caller
// cannot see exists. It returns another error, which the caller answers with
// 503, when server is in identity's view but the core's lists leave it out for
// its health status (health.ShouldAdvertise admits healthy, degraded and
// unmonitored backends), when the method's list cannot be listed on server now
// (core.AdmittedBackendCapabilities.ListErrors), or when the view cannot be
// built now, such as when no backend answers discovery, which every server
// name gets alike.
func (s *Server) callBindingListItems(
	ctx context.Context, identity *auth.Identity, method, server string,
) ([]json.RawMessage, error) {
	if !toolcalldetails.IsListMethod(method) {
		return nil, fmt.Errorf("call binding: %q is not a list method", method)
	}
	caps, err := core.LookupBackendCapabilities(ctx, s.core, identity, server)
	switch {
	case errors.Is(err, core.ErrBackendCapabilitiesLookupUnsupported):
		caps, err = s.callBindingListsCapabilities(ctx, identity, method, server)
	case errors.Is(err, vmcp.ErrNotFound):
		return nil, errListServerNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := caps.ListErrors[method]; err != nil {
		return nil, fmt.Errorf("call binding: the list call's server %q cannot list %s now: %w", server, method, err)
	}
	switch method {
	case "tools/list":
		if s.optimizerFactory != nil {
			return []json.RawMessage{}, nil
		}
		return renderListItems(caps.Tools, modernToolFromDomain)
	case "prompts/list":
		return renderListItems(caps.Prompts, func(p vmcp.Prompt) (any, error) {
			return modernPromptFromDomain(p), nil
		})
	case "resources/list":
		return renderListItems(caps.Resources, func(r vmcp.Resource) (any, error) {
			return modernResourceFromDomain(r), nil
		})
	default: // resources/templates/list
		return renderListItems(caps.ResourceTemplates, func(t vmcp.ResourceTemplate) (any, error) {
			return modernResourceTemplateFromDomain(t), nil
		})
	}
}

// callBindingListsCapabilities is callBindingListItems' answer for a core
// that does not implement core.BackendCapabilitiesLookup: server's items from
// the core's own list for method, after callBindingListServer's check. The
// check and the items come from separate aggregations, so a backend that drops
// out of the view between the two is answered with an empty list, and a list
// that failed transiently on server reads as empty.
func (s *Server) callBindingListsCapabilities(
	ctx context.Context, identity *auth.Identity, method, server string,
) (*core.AdmittedBackendCapabilities, error) {
	if err := s.callBindingListServer(ctx, identity, server); err != nil {
		return nil, err
	}
	switch method {
	case "tools/list":
		tools, err := s.core.ListTools(ctx, identity)
		if err != nil {
			return nil, err
		}
		return &core.AdmittedBackendCapabilities{
			Tools: ofServer(tools, server, func(t vmcp.Tool) string { return t.BackendID }),
		}, nil
	case "prompts/list":
		prompts, err := s.core.ListPrompts(ctx, identity)
		if err != nil {
			return nil, err
		}
		return &core.AdmittedBackendCapabilities{
			Prompts: ofServer(prompts, server, func(p vmcp.Prompt) string { return p.BackendID }),
		}, nil
	case "resources/list":
		resources, err := s.core.ListResources(ctx, identity)
		if err != nil {
			return nil, err
		}
		return &core.AdmittedBackendCapabilities{
			Resources: ofServer(resources, server, func(r vmcp.Resource) string { return r.BackendID }),
		}, nil
	default: // resources/templates/list
		templates, err := s.core.ListResourceTemplates(ctx, identity)
		if err != nil {
			return nil, err
		}
		return &core.AdmittedBackendCapabilities{
			ResourceTemplates: ofServer(templates, server, func(t vmcp.ResourceTemplate) string { return t.BackendID }),
		}, nil
	}
}

// ofServer returns a new slice of the items whose backend is server, in order.
func ofServer[T any](items []T, server string, backend func(T) string) []T {
	return slices.DeleteFunc(slices.Clone(items), func(item T) bool { return backend(item) != server })
}

// callBindingListServer checks that server is a backend in identity's view
// that the core's lists can include now, for callBindingListsCapabilities. It
// returns errListServerNotFound when server is unknown, hidden from identity
// by authorization, offers identity nothing, or contributes nothing to the
// view (its discovery fails). A backend's resource templates count, which
// LookupBackend does not count. It returns another error for a backend in
// identity's view that the core's lists leave out for its health status:
// health.ShouldAdvertise admits healthy, degraded and unmonitored backends,
// read from the health monitor and else from the registry, as the lists do.
func (s *Server) callBindingListServer(ctx context.Context, identity *auth.Identity, server string) error {
	backend, err := s.core.LookupBackend(ctx, identity, server)
	switch {
	case errors.Is(err, vmcp.ErrNotFound):
		templates, err := s.core.ListResourceTemplates(ctx, identity)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(templates, func(t vmcp.ResourceTemplate) bool { return t.BackendID == server }) {
			// Only templates: they come from the lists, so the backend is in them.
			return nil
		}
		return errListServerNotFound
	case err != nil:
		return err
	}
	status := backend.HealthStatus
	if monitor := s.core.BackendHealth(); monitor != nil {
		if monitored, ok := monitor.QueryBackendStatus(server); ok {
			status = monitored
		}
	}
	if !health.ShouldAdvertise(status) {
		return fmt.Errorf("call binding: the list call's server is %s", status)
	}
	return nil
}

// renderListItems renders items in their wire form, as list result items.
func renderListItems[T, W any](items []T, render func(T) (W, error)) ([]json.RawMessage, error) {
	rendered := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		wire, err := render(item)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(wire)
		if err != nil {
			return nil, err
		}
		rendered = append(rendered, raw)
	}
	return rendered, nil
}

// callBindingMiddleware enforces call-bound access tokens. It runs after
// authentication and parsing and before classification, so it covers Legacy
// and Modern requests alike, and before session ownership, the authorization
// CallGate, Cedar and dispatch. It can only refuse: a request it passes goes
// on through every later check. So a request it refuses gets its 401 or 403
// even on a session the caller does not own; a request it passes still gets
// the ownership 404.
//
// A token without an mcp_tool_call entry passes unchanged, unless its client
// is listed in CallBinding.Clients. A token with a tool-call entry may send, to
// the MCP endpoint only:
//   - the handshake (initialize, notifications/initialized), the Modern
//     server/discover probe, and ping; initialize only until the token has
//     started its call;
//   - tools/list, with a tool-call entry; it is filtered by the usual
//     admission and does not use up the token;
//   - GET and DELETE, which the session-ownership check then limits to the
//     sessions the token's principal owns, and which, once the token has
//     started its call, are limited to that call's session; neither uses up
//     the token;
//   - one tools/call that matches its entry, which uses up the token;
//   - on the Legacy session of that call, while the call is open (its request
//     is open, or its dispatch still runs on this replica): a
//     notifications/cancelled for the call's ID, and a JSON-RPC response
//     (such as an elicitation reply).
//
// After the token's exp, every request gets 401 unless the token's iss and
// client_id match an entry of the binding's ExpiredTokenAdmission list, which
// is empty by default. For a matching token, the token validator accepts a
// request only when the binding admits it
// (callbinding.Binding.AdmitExpiredToken), and marks the identity
// (auth.Identity.AdmittedAfterExpiry). Such a request may be only one of the
// open call's own messages, on its session, within the entry's grace after exp,
// while the call is open and younger than the maximum call duration: a GET or
// DELETE, a notifications/cancelled for the call's ID, or, for a tool call, a
// JSON-RPC response (see serveAfterExpiry). For such a token, exp limits only
// when a call can start.
//
// A token with a list-call entry may send the handshake, server/discover, ping,
// GET and DELETE as above, and the list method its entry names once, answered
// with all of the entry's server's items (see serveListCall). That list call
// is the token's one call: it uses the token up, as a tools/call does. It is
// recorded only once its answer is ready, so no message is admitted for it.
//
// Everything else is refused (see refuse), and everything else after the
// token's exp with 401. For a tools/call, a later refusal, such as the
// session-ownership 404 or a Cedar 403, still uses up the token: the client
// asks for a new token rather than retrying. So does a retried tools/call,
// such as the retry after a URL elicitation: each attempt needs its own token.
// A list call that a later check refuses gives no answer and is not recorded.
func (s *Server) callBindingMiddleware(next http.Handler) http.Handler {
	cb := s.callBinding
	if cb == nil {
		// Serve always sets it.
		cb = newCallBinding(nil, s.callBindingRoute, s.callBindingListItems)
	}
	endpointPath := cmp.Or(s.config.EndpointPath, defaultEndpointPath)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := auth.IdentityFromContext(r.Context())
		if identity == nil {
			next.ServeHTTP(w, r)
			return
		}
		parsed := mcpparser.GetParsedMCPRequest(r.Context())
		cb.warnIfNoClient(identity.Claims)
		admitted := !identity.AdmittedAfterExpiry.IsZero()
		token, reason := cb.binding.Token(identity.Claims)
		if token == nil && reason == "" && admitted {
			// Only a call-bound token's call has messages after exp.
			reason = callbinding.ReasonTokenExpired
		}
		if reason != "" {
			cb.refuse(w, r, parsed, &callbinding.Token{ClientID: callbinding.ClientID(identity.Claims)}, reason)
			return
		}
		if token == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != endpointPath {
			// After exp, everything but the call's own messages is refused as
			// the token validator refuses an expired token.
			reason = callbinding.ReasonMethodNotAllowed
			if admitted {
				reason = callbinding.ReasonTokenExpired
			}
			cb.refuse(w, r, parsed, token, reason)
			return
		}
		cb.serveBound(w, r, next, identity, token, parsed)
	})
}

// warnIfNoClient logs once when clients are listed and a token names its
// client in neither claim callbinding.ClientID reads, so such a token is
// never required to carry an entry.
func (cb *callBinding) warnIfNoClient(claims map[string]any) {
	if !cb.binding.ClientsListed() || callbinding.ClientID(claims) != "" {
		return
	}
	cb.noClientWarning.Do(func() {
		slog.Warn("call binding: CallBinding.Clients is set, but an access token names its client in " +
			"neither client_id nor azp, so it is not required to carry an mcp_tool_call entry; " +
			"logged once")
	})
}

// serveBound handles a request to the MCP endpoint that carries a call-bound
// token.
func (cb *callBinding) serveBound(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	// A token the validator accepted after it expired, or one that expired
	// between the validator and here, may send only its open call's messages.
	if !identity.AdmittedAfterExpiry.IsZero() || !cb.now().Before(token.ExpiresAt.Add(callBindingLeeway)) {
		cb.serveAfterExpiry(w, r, next, identity, token, parsed)
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		cb.serveSessionRequest(w, r, next, token, parsed)
	case http.MethodPost:
		// ParsingMiddleware parses every POST and answers one it cannot
		// parse, so a POST without a parsed request is a JSON-RPC response.
		if parsed == nil {
			cb.serveCallMessage(w, r, next, token, parsed, "")
			return
		}
		cb.servePost(w, r, next, identity, token, parsed)
	default:
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
	}
}

// serveAfterExpiry handles a request that the token validator accepted after
// the token expired, because the binding admitted it, or whose token expired
// since the validator. It admits only a token whose iss and client_id match an
// ExpiredTokenAdmission entry, within that entry's grace after exp, and only
// a message of the call the token started, on that call's session, while the
// call is open and younger than the maximum call duration: a GET or DELETE, a
// notifications/cancelled for the call's ID, or, for a tool call, a JSON-RPC
// response, such as an elicitation reply. A list call is recorded ended, so it
// has none. The checks the binding made are made again, as the call may have
// ended since. Anything else is refused with 401 token_expired, as the token
// validator refuses an expired token: no new call, list, initialize or ping.
//
// A GET on the call's session lets a client whose stream dropped receive the
// call's messages again, and a DELETE ends the session; neither starts
// anything. Like a stream opened before exp, a stream the GET opens is checked
// only when it opens: it keeps running after the call ends, until the client
// or the session closes it. Admitting any response on the call's session is a shortcut, as it
// is before exp (see serveCallMessage).
func (cb *callBinding) serveAfterExpiry(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	// The validator and this check are wired separately, so the list is
	// checked here too: without a matching entry, exp is strict.
	if !cb.binding.WithinExpiredTokenGrace(identity.Claims, token.ExpiresAt, cb.now()) {
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenExpired)
		return
	}
	call, open, err := cb.binding.OpenCall(r.Context(), token.Key, cb.now())
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	}
	if !open || !belongsToCall(r, call, "") || !isCallMessageAfterExpiry(r, token, parsed, call) {
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenExpired)
		return
	}
	mcpMethod := ""
	if parsed != nil {
		mcpMethod = parsed.Method
	}
	slog.DebugContext(r.Context(), "call binding: admitted a message of an open call after its token expired",
		"jti", token.JTI, "operation_id", token.Entry.OperationID, "http_method", r.Method, "mcp_method", mcpMethod)
	next.ServeHTTP(w, r)
}

// isCallMessageAfterExpiry reports whether r, on call's session, is one of
// the messages serveAfterExpiry admits for call.
func isCallMessageAfterExpiry(
	r *http.Request, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest, call callbinding.Call,
) bool {
	switch r.Method {
	case http.MethodGet, http.MethodDelete:
		// A list call has no messages after exp. It is recorded ended, but
		// with a second write that a shared record may miss, so the entry
		// type decides.
		return !token.Entry.IsList()
	case http.MethodPost:
		if parsed == nil {
			// A JSON-RPC response (see serveBound). A list call asks the
			// client nothing.
			return !token.Entry.IsList()
		}
		if parsed.Method != "notifications/cancelled" {
			return false
		}
		requestID, ok := cancelledRequestID(parsed.Params)
		return ok && requestID == call.RequestID
	default:
		return false
	}
}

// servePost handles a JSON-RPC request or notification sent with a
// call-bound token.
func (cb *callBinding) servePost(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	switch {
	case parsed.Method == "initialize":
		cb.serveInitialize(w, r, next, token, parsed)
	case slices.Contains(callBindingPassMethods, parsed.Method):
		next.ServeHTTP(w, r)
	case parsed.Method == "notifications/cancelled":
		requestID, ok := cancelledRequestID(parsed.Params)
		if !ok {
			cb.refuse(w, r, parsed, token, callbinding.ReasonNoOpenCall)
			return
		}
		cb.serveCallMessage(w, r, next, token, parsed, requestID)
	case token.Entry.IsList():
		cb.serveListCall(w, r, next, identity, token, parsed)
	case parsed.Method == "tools/list":
		// Read-only and filtered by the usual admission. MCP clients list
		// tools around a call, for example to read a tool's output schema.
		next.ServeHTTP(w, r)
	case parsed.Method == "tools/call":
		cb.serveToolCall(w, r, next, identity, token, parsed)
	case toolcalldetails.IsListMethod(parsed.Method):
		// A tool-call token never sends another list method; once it has
		// started its call, the client needs a new token above all.
		if !cb.refusedAsReused(w, r, token, parsed) {
			cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
		}
	default:
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
	}
}

// refusedAsReused answers a request that the token can no longer send
// because it has started its call: 401 token_reused, which tells the client
// it needs a new token, rather than a refusal about the request. It reports
// whether it answered (503 when the record fails).
func (cb *callBinding) refusedAsReused(
	w http.ResponseWriter, r *http.Request, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) bool {
	_, started, err := cb.binding.Record().Lookup(r.Context(), token.Key)
	switch {
	case err != nil:
		cb.unavailable(w, r, parsed, token, "record", err)
		return true
	case started:
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenReused)
		return true
	}
	return false
}

// serveListCall handles a request, other than the handshake, server/discover
// and ping, sent with a list-call token. Once the token has started its call,
// any list method or tools/call is refused with token_reused. Before that, it
// admits only the entry's list method: a tools/call or another list method is
// refused with method_mismatch, and any other request with method_not_allowed.
//
// The answer holds all of the entry's server's items, in one response without
// nextCursor. A list request names no server, so the binding is applied to the
// answer: its items come from callBindingListItems, attributed by their own
// backend in one core view, and replace the items of the response the usual
// handler gives. That handler still runs, after every later check, so a list
// call answers only on a session the caller owns, with the revision's own
// envelope. Its own items, which a Legacy session takes from its store and
// either revision cuts into pages whose cursor names the last unfiltered
// item, never reach the client, and neither does a cursor. A request that
// carries its own cursor is refused with invalid_params.
//
// The list call is the token's one call, as a tools/call is for a tool-call
// token. It is recorded only once its answer is ready, and ended at once: of
// concurrent duplicates, each runs the handler, but only the first to record
// delivers its answer, and the others are refused with token_reused. So a
// list call that gives the client no list leaves the token unused: a refusal,
// a server not in the caller's view (server_not_found) or not served now
// (503), a JSON-RPC error, a later check's refusal, a response that cannot be
// read, a client that went away, or a handler that panics. A message for a
// list call in flight gets no_open_call, since the call is not yet recorded.
func (cb *callBinding) serveListCall(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	requestID, items, ok := cb.admitListCall(w, r, identity, token, parsed)
	if !ok {
		return
	}
	buffered := &listResponseBuffer{header: http.Header{}, status: http.StatusOK}
	next.ServeHTTP(buffered, r)
	answer, ok := cb.listAnswer(w, r, token, parsed, buffered, items)
	if !ok {
		return
	}
	// Only now, with a list to deliver, is the call recorded: of concurrent
	// duplicates, the one that records first delivers its answer and the
	// others are refused. The answer is complete, so the call ends at once.
	expiry, ok := cb.startCall(w, r, token, parsed, requestID)
	if !ok {
		return
	}
	cb.binding.EndCall(r.Context(), token, expiry)
	slog.DebugContext(r.Context(), "call binding: answered a call-bound list call",
		"jti", token.JTI, "client_id", token.ClientID, "server", token.Entry.Server, "method", token.Entry.Method)
	copyHeader(w.Header(), buffered.header)
	w.Header().Set("Content-Type", "application/json")
	writeListResponse(w, buffered.status, answer)
}

// admitListCall runs the checks of a list call that come before the handler
// runs, and returns its request ID and its answer's items. When a check
// refuses the request, or the items cannot be listed, it answers the request
// and returns false.
func (cb *callBinding) admitListCall(
	w http.ResponseWriter, r *http.Request,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) (string, []json.RawMessage, bool) {
	reason := listCallRefusal(token, parsed)
	if reason == callbinding.ReasonMethodNotAllowed {
		// No list-call token may send it, spent or not.
		cb.refuse(w, r, parsed, token, reason)
		return "", nil, false
	}
	if cb.refusedAsReused(w, r, token, parsed) {
		return "", nil, false
	}
	if reason != "" {
		cb.refuse(w, r, parsed, token, reason)
		return "", nil, false
	}
	requestID, ok := requestIDKey(parsed.ID)
	if !ok {
		// A list notification has no ID and gets no answer; nothing
		// legitimate sends one.
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
		return "", nil, false
	}
	if hasCursor, err := callbinding.ListParamsCarryCursor(parsed.Params); err != nil || hasCursor {
		cb.refuse(w, r, parsed, token, callbinding.ReasonInvalidParams)
		return "", nil, false
	}
	items, err := cb.listItems(r.Context(), identity, parsed.Method, token.Entry.Server)
	switch {
	case errors.Is(err, errListServerNotFound):
		cb.refuse(w, r, parsed, token, callbinding.ReasonServerNotFound)
		return "", nil, false
	case err != nil:
		cb.unavailable(w, r, parsed, token, "list", err)
		return "", nil, false
	}
	return requestID, items, true
}

// listAnswer turns the handler's buffered response to a list call into the
// answer, with items in place of the response's own items, and returns it.
// When the response holds no list, it answers the request itself and returns
// false: with the response as it is for any status other than 2xx, an empty
// body or a JSON-RPC error; with a 503 when the client went away, whatever the
// handler wrote, which records the call as an error in the audit log; and
// with a 500 for anything else. A Legacy session the SDK rehydrated answers in
// text/event-stream; the answer is JSON either way.
func (cb *callBinding) listAnswer(
	w http.ResponseWriter, r *http.Request, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
	buffered *listResponseBuffer, items []json.RawMessage,
) ([]byte, bool) {
	if r.Context().Err() != nil {
		slog.DebugContext(r.Context(), "call binding: a list call was cancelled before it was answered",
			"jti", token.JTI)
		writeCallBindingError(w, parsed, http.StatusServiceUnavailable, mcpparser.CodeInternalError,
			"Service unavailable: the list call was cancelled before it was answered")
		return nil, false
	}
	body := buffered.body.Bytes()
	if buffered.status < http.StatusOK || buffered.status >= http.StatusMultipleChoices || len(body) == 0 {
		// A refusal by a later check, such as the session-ownership 404.
		copyHeader(w.Header(), buffered.header)
		writeListResponse(w, buffered.status, body)
		return nil, false
	}
	payload := body
	if isEventStream(buffered.header) {
		var err error
		if payload, err = callbinding.SSEResponse(body); err != nil {
			cb.listFailed(w, r, parsed, token, buffered.status, err)
			return nil, false
		}
	}
	resp, isError, err := callbinding.ReadListResponse(parsed.Method, payload)
	switch {
	case isError:
		copyHeader(w.Header(), buffered.header)
		writeListResponse(w, buffered.status, body)
		return nil, false
	case err != nil:
		cb.listFailed(w, r, parsed, token, buffered.status, err)
		return nil, false
	}
	answer, err := resp.Encode(items)
	if err != nil {
		cb.listFailed(w, r, parsed, token, buffered.status, err)
		return nil, false
	}
	return answer, true
}

// isEventStream reports whether header labels the body text/event-stream.
func isEventStream(header http.Header) bool {
	mediaType, _, _ := strings.Cut(header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(mediaType), "text/event-stream")
}

// copyHeader copies the handler's headers onto the response, but for
// Content-Length, which writeListResponse leaves to net/http.
func copyHeader(dst, src http.Header) {
	for name, values := range src {
		if !strings.EqualFold(name, "Content-Length") {
			dst[name] = slices.Clone(values)
		}
	}
}

// listCallRefusal returns the reason to refuse a request sent with a
// list-call token, or "" for the entry's own list method: method_mismatch for
// a tools/call or another list method, method_not_allowed for anything else.
func listCallRefusal(token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest) string {
	switch {
	case parsed.Method == token.Entry.Method:
		return ""
	case parsed.Method == "tools/call" || toolcalldetails.IsListMethod(parsed.Method):
		return callbinding.ReasonMethodMismatch
	default:
		return callbinding.ReasonMethodNotAllowed
	}
}

// listFailed answers a list call whose handler answered with something that
// is not a list, with a 500 rather than anything of that answer.
func (*callBinding) listFailed(
	w http.ResponseWriter, r *http.Request, parsed *mcpparser.ParsedMCPRequest,
	token *callbinding.Token, status int, err error,
) {
	slog.ErrorContext(r.Context(), "call binding: could not answer a list call: the handler gave no list",
		"jti", token.JTI, "mcp_method", parsed.Method, "status", status, "error", err)
	writeCallBindingError(w, parsed, http.StatusInternalServerError, mcpparser.CodeInternalError,
		"Internal error: the list could not be limited to the access token's server")
}

// writeListResponse writes the answer to a list call.
func writeListResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Del("Content-Length")
	w.WriteHeader(status)
	//nolint:gosec // G705: a JSON-RPC response, not rendered as HTML
	_, _ = w.Write(body)
}

// listResponseBuffer holds the response to a list call until serveListCall has
// replaced its items. Its headers, status and body are its own: they reach the
// real writer only with the response they belong to. Flush is a no-op, and it
// has no Unwrap, so nothing reaches the real writer before the answer is
// complete.
type listResponseBuffer struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (b *listResponseBuffer) Header() http.Header { return b.header }

func (b *listResponseBuffer) WriteHeader(status int) {
	if !b.wroteHeader {
		b.status = status
		b.wroteHeader = true
	}
}

func (b *listResponseBuffer) Write(p []byte) (int, error) {
	b.wroteHeader = true
	return b.body.Write(p)
}

// Flush does nothing: the response is sent once it has been filtered.
func (*listResponseBuffer) Flush() {}

// serveInitialize admits initialize until the token has started its call: a
// spent token has no call left to open a session for.
func (cb *callBinding) serveInitialize(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	_, started, err := cb.binding.Record().Lookup(r.Context(), token.Key)
	switch {
	case err != nil:
		cb.unavailable(w, r, parsed, token, "record", err)
	case started:
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenReused)
	default:
		next.ServeHTTP(w, r)
	}
}

// serveSessionRequest admits a GET or DELETE. Once the token has started its
// call, it admits them only on that call's session (none for a Modern call).
// Before that, session ownership alone limits them.
func (cb *callBinding) serveSessionRequest(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	call, started, err := cb.binding.Record().Lookup(r.Context(), token.Key)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	}
	if started {
		sessionID, err := sessionbinding.RequestID(r, "")
		if err != nil || sessionID != call.SessionID {
			cb.refuse(w, r, parsed, token, callbinding.ReasonSessionMismatch)
			return
		}
	}
	next.ServeHTTP(w, r)
}

// serveToolCall admits the one tools/call the token's entry names and records
// the token as used.
func (cb *callBinding) serveToolCall(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	ctx := r.Context()
	requestID, ok := requestIDKey(parsed.ID)
	if !ok {
		// A tools/call notification has no ID and starts no call the client
		// could see; nothing legitimate sends one.
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
		return
	}
	reason, err := cb.checkToolCall(ctx, identity, token, parsed)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "route", err)
		return
	}
	if reason != "" {
		// A token that has started its call needs a new one, whatever the
		// request names. On the matching path, Start finds the reuse.
		if !cb.refusedAsReused(w, r, token, parsed) {
			cb.refuse(w, r, parsed, token, reason)
		}
		return
	}

	expiry, ok := cb.startCall(w, r, token, parsed, requestID)
	if !ok {
		return
	}
	defer cb.binding.EndCall(ctx, token, expiry)
	cb.warnIfValidatorUnwired(ctx)
	slog.DebugContext(ctx, "call binding: admitted a call-bound tools/call",
		"jti", token.JTI, "client_id", token.ClientID,
		"operation_id", token.Entry.OperationID, "server", token.Entry.Server, "tool", token.Entry.Tool)
	// The call is open while its request is. On Modern that is the whole
	// call. On Legacy, closing the POST returns the request but not the
	// dispatch, so the call stays open while its dispatch runs on this replica
	// (callBinding.dispatching), and a later cancellation still reaches it.
	next.ServeHTTP(w, r)
}

// dispatching marks a Legacy tools/call dispatch for identity's call-bound
// token as running, and returns the function that marks it done; see
// callbinding.Binding.BeginDispatch. It does nothing for any other identity.
func (cb *callBinding) dispatching(identity *auth.Identity) (done func()) {
	if cb == nil || identity == nil {
		return func() {}
	}
	token, reason := cb.binding.Token(identity.Claims)
	if reason != "" || token == nil || token.Entry.IsList() {
		return func() {}
	}
	return cb.binding.BeginDispatch(token)
}

// warnIfValidatorUnwired logs once when a call-bound tool call starts, the
// binding admits some tokens after exp, and the token validator was never
// given the binding's option: the call's messages then get the validator's
// 401 once the token expires, whatever the ExpiredTokenAdmission list says.
func (cb *callBinding) warnIfValidatorUnwired(ctx context.Context) {
	if !cb.binding.AdmitsExpiredTokens() || cb.binding.ValidatorWired() {
		return
	}
	cb.unwiredWarning.Do(func() {
		slog.WarnContext(ctx, "call binding: a call-bound tool call started, but the token validator was not "+
			"given the call binding's TokenValidatorOption (factory.WithCallBinding), so the call's "+
			"cancellation and other messages are refused once its token expires; logged once")
	})
}

// startCall records the token as having started the call that requestID
// names, on the request's session. It returns the record's expiry and true,
// or answers the request and returns false: token_reused when the token has
// started a call already, 503 when the record fails. The caller ends the call
// with endCall once its request returns.
func (cb *callBinding) startCall(
	w http.ResponseWriter, r *http.Request, token *callbinding.Token,
	parsed *mcpparser.ParsedMCPRequest, requestID string,
) (time.Time, bool) {
	now := cb.now()
	expiry := cb.recordExpiry(token, now)
	call := callbinding.Call{SessionID: callSessionID(r, parsed), RequestID: requestID, Started: now}
	started, err := cb.binding.Record().Start(r.Context(), token.Key, call, expiry)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return time.Time{}, false
	}
	if !started {
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenReused)
		return time.Time{}, false
	}
	return expiry, true
}

// checkToolCall checks a tools/call against the token's entry. It returns
// the reason to refuse it with, or "" when the call matches, and an error when
// the route lookup fails for a reason other than authorization.
func (cb *callBinding) checkToolCall(
	ctx context.Context, identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) (string, error) {
	// Read params as received, before routing, prefix stripping or any
	// rewrite, so the hash covers what the client sent.
	name, arguments, err := toolCallParams(parsed.Params)
	if err != nil {
		return callbinding.ReasonInvalidParams, nil
	}
	if name != token.Entry.Tool {
		return callbinding.ReasonToolMismatch, nil
	}
	hash, err := callbinding.ArgumentsSHA256(arguments)
	if err != nil {
		return callbinding.ReasonInvalidArguments, nil
	}
	if hash != token.Entry.ArgumentsSHA256 {
		return callbinding.ReasonArgumentsMismatch, nil
	}
	backendID, err := cb.route(ctx, identity, name)
	switch {
	case errors.Is(err, vmcp.ErrNotFound):
		// Unknown, unadvertised, or hidden from this caller by Cedar. The
		// caller already holds a token naming this tool, so telling these
		// apart from a routed tool reveals nothing it could not ask for.
		return callbinding.ReasonToolNotFound, nil
	case err != nil:
		return "", err
	case backendID == "":
		return callbinding.ReasonToolNotBindable, nil
	case backendID != token.Entry.Server:
		return callbinding.ReasonServerMismatch, nil
	}
	return "", nil
}

// callSessionID is the session a call runs on: the Mcp-Session-Id of a
// Legacy request, read as the session-ownership check reads it. A Modern
// request has no session, whatever header it sends: Modern dispatch never
// reads one, and session ownership checks it only on Legacy. A request whose
// revision or session ID is ambiguous is recorded without a session; a later
// check refuses it, and no call message is admitted for it.
func callSessionID(r *http.Request, parsed *mcpparser.ParsedMCPRequest) string {
	rev, err := mcpparser.ClassifyRevision(parsed.Method, parsed.Meta, r.Header.Get("MCP-Protocol-Version"))
	if err != nil || rev == mcpparser.RevisionModern {
		return ""
	}
	sessionID, err := sessionbinding.RequestID(r, "")
	if err != nil {
		return ""
	}
	return sessionID
}

// recordExpiry is how long the record keeps a token started at now: until the
// token can no longer be accepted, or until the call has run for the maximum
// call duration, whichever is later, plus callBindingRetentionMargin.
func (cb *callBinding) recordExpiry(token *callbinding.Token, now time.Time) time.Time {
	expiry := token.ExpiresAt.Add(callBindingLeeway)
	if callEnd := now.Add(cb.binding.MaxCallDuration()); callEnd.After(expiry) {
		expiry = callEnd
	}
	return expiry.Add(callBindingRetentionMargin)
}

// serveCallMessage admits a message that belongs to the call the token
// started, on that call's session, while the call is open (see
// callbinding.Binding.OpenCall) and
// younger than the maximum call duration: a notifications/cancelled for the
// call's ID (requestID non-empty), or a JSON-RPC response (requestID empty).
//
// A call without a session (Modern) has no messages: a Modern client cancels
// by closing the request and answers input requests by retrying the call.
//
// Admitting any response on the call's session is a shortcut: the vMCP does
// not see which server-to-client requests the SDK is waiting on, so the
// session is the only binding (see "vMCP: bind the session to act", #6888).
func (cb *callBinding) serveCallMessage(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest, requestID string,
) {
	call, open, err := cb.binding.OpenCall(r.Context(), token.Key, cb.now())
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	}
	// A list call asks the client nothing, so no response belongs to it.
	listResponse := requestID == "" && token.Entry.IsList()
	if listResponse || !open || !belongsToCall(r, call, requestID) {
		cb.refuse(w, r, parsed, token, callbinding.ReasonNoOpenCall)
		return
	}
	next.ServeHTTP(w, r)
}

// belongsToCall reports whether a message sent as r is on call's session and,
// for a cancellation (requestID non-empty), names call's request.
func belongsToCall(r *http.Request, call callbinding.Call, requestID string) bool {
	return callbinding.OnCallSession(r, call) && (requestID == "" || requestID == call.RequestID)
}

// refuse answers a refused request and marks it so the audit middleware
// records it as a denial with its reason. A reason in
// callBindingTokenReasons gets 401 with an invalid_token challenge and an RFC
// 6750 error body, as the token validator answers a token it refuses; every
// other reason gets 403 with a JSON-RPC error and no challenge. The log
// carries the token's jti, never the token or the arguments.
func (cb *callBinding) refuse(
	w http.ResponseWriter, r *http.Request, parsed *mcpparser.ParsedMCPRequest,
	token *callbinding.Token, reason string,
) {
	if marker, ok := mcpparser.AuthzDenialMarkerFromContext(r.Context()); ok {
		marker.Denied = true
		marker.Reason = reason
	}
	mcpMethod := ""
	if parsed != nil {
		mcpMethod = parsed.Method
	}
	slog.WarnContext(r.Context(), "call binding: refused a request",
		"reason", reason, "http_method", r.Method, "mcp_method", mcpMethod,
		"jti", token.JTI, "client_id", token.ClientID, "operation_id", token.Entry.OperationID,
		"server", token.Entry.Server, "tool", token.Entry.Tool, "method", token.Entry.Method)

	if slices.Contains(callBindingTokenReasons, reason) {
		cb.binding.Challenge().WriteInvalidToken(w, "the access token is not accepted ("+reason+")")
		return
	}
	writeCallBindingError(w, parsed, http.StatusForbidden, mcpparser.JSONRPCCodeDenied,
		"Forbidden: the access token does not permit this request ("+reason+")")
}

// unavailable answers 503 when the check cannot be completed. The token is
// not used up, so the client may retry with it. Like the session-ownership
// check, it logs at DEBUG when the client has gone away.
func (*callBinding) unavailable(
	w http.ResponseWriter, r *http.Request, parsed *mcpparser.ParsedMCPRequest,
	token *callbinding.Token, step string, err error,
) {
	level := slog.LevelWarn
	if r.Context().Err() != nil {
		level = slog.LevelDebug
	}
	slog.Log(r.Context(), level, "call binding: could not check a call-bound token",
		"step", step, "jti", token.JTI, "error", err)
	writeCallBindingError(w, parsed, http.StatusServiceUnavailable, mcpparser.CodeInternalError,
		"Service unavailable: the access token could not be checked")
}

// writeCallBindingError writes a JSON-RPC error answering parsed, without an
// ID when the request has none.
func writeCallBindingError(
	w http.ResponseWriter, parsed *mcpparser.ParsedMCPRequest, status int, code int64, message string,
) {
	resp := &jsonrpc2.Response{Error: jsonrpc2.NewError(code, message)}
	if parsed != nil {
		if id, err := mcpparser.ConvertToJSONRPC2ID(parsed.ID); err == nil {
			resp.ID = id
		}
	}
	//nolint:gosec // G104: writing a JSON-RPC error response to an HTTP client
	_ = mcpparser.WriteJSONRPCError(w, status, resp)
}
