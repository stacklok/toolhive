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
	"maps"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/mcp/toolcalldetails"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/callbinding"
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

// callBindingEndTimeout bounds marking a call as ended in the record. The
// request context may already be cancelled when the call returns, so the
// record gets a context of its own, as the session manager gives its storage.
const callBindingEndTimeout = 5 * time.Second

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

// callBindingListItems returns the keys (see callbinding.FilterListResult) of
// the items that the list method lists for identity from the backend named
// server.
type callBindingListItems func(
	ctx context.Context, identity *auth.Identity, method, server string,
) (map[string]struct{}, error)

// callBinding enforces access tokens that carry an RFC 9396
// authorization_details entry of type https://toolhive.dev/mcp_tool_call (see
// pkg/mcp/toolcalldetails). A token with a tool-call entry may start one
// tools/call, the one its entry names, and only once. A token with a list-call
// entry may send the list method its entry names, answered with only its
// server's items.
type callBinding struct {
	required        map[string]struct{}
	maxCallDuration time.Duration
	record          callbinding.Record
	route           callBindingRoute
	listItems       callBindingListItems
	challenge       auth.BearerChallenge
	now             func() time.Time

	// returned holds the calls whose request has returned on this replica
	// but that the record could not mark as ended.
	returned returnedCalls
	// noClientWarning logs once that Clients is set and a token names no
	// client.
	noClientWarning sync.Once
}

// newCallBinding validates cfg and builds the enforcement it describes.
func newCallBinding(
	cfg callbinding.Config, route callBindingRoute, listItems callBindingListItems,
) (*callBinding, error) {
	if cfg.MaxCallDuration < 0 {
		return nil, fmt.Errorf("%w: CallBinding.MaxCallDuration must not be negative", vmcp.ErrInvalidConfig)
	}
	if slices.Contains(cfg.Clients, "") {
		return nil, fmt.Errorf("%w: CallBinding.Clients must not contain an empty client ID", vmcp.ErrInvalidConfig)
	}
	required := make(map[string]struct{}, len(cfg.Clients))
	for _, client := range cfg.Clients {
		required[client] = struct{}{}
	}
	record := cfg.Record
	if record == nil {
		record = callbinding.NewMemoryRecord()
	}
	challenge := cfg.Challenge
	challenge.Scopes = slices.Clone(challenge.Scopes)
	return &callBinding{
		required:        required,
		maxCallDuration: cmp.Or(cfg.MaxCallDuration, callbinding.DefaultMaxCallDuration),
		record:          record,
		route:           route,
		listItems:       listItems,
		challenge:       challenge,
		now:             time.Now,
	}, nil
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

// callBindingListItems lists, for a list-call token, the keys of the items
// method lists from server for identity, from the same admission-filtered core
// lists that dispatch serves. Only items whose BackendID is server count, so
// a composite tool, code mode's execute_tool_script and, with the optimizer
// enabled, the find_tool and call_tool meta-tools are never listed: with the
// optimizer, no tool counts at all.
func (s *Server) callBindingListItems(
	ctx context.Context, identity *auth.Identity, method, server string,
) (map[string]struct{}, error) {
	switch method {
	case "tools/list":
		if s.optimizerFactory != nil {
			return map[string]struct{}{}, nil
		}
		tools, err := s.core.ListTools(ctx, identity)
		return serverKeys(tools, err, server, func(t vmcp.Tool) (string, string) { return t.BackendID, t.Name })
	case "prompts/list":
		prompts, err := s.core.ListPrompts(ctx, identity)
		return serverKeys(prompts, err, server, func(p vmcp.Prompt) (string, string) { return p.BackendID, p.Name })
	case "resources/list":
		resources, err := s.core.ListResources(ctx, identity)
		return serverKeys(resources, err, server, func(r vmcp.Resource) (string, string) { return r.BackendID, r.URI })
	case "resources/templates/list":
		templates, err := s.core.ListResourceTemplates(ctx, identity)
		return serverKeys(templates, err, server, func(t vmcp.ResourceTemplate) (string, string) {
			return t.BackendID, t.URITemplate
		})
	default:
		return nil, fmt.Errorf("call binding: %q is not a list method", method)
	}
}

// serverKeys returns the keys of the items whose backend is server, or err
// when listing them failed. keyOf returns an item's backend and key.
func serverKeys[T any](
	items []T, err error, server string, keyOf func(T) (backend, key string),
) (map[string]struct{}, error) {
	if err != nil {
		return nil, err
	}
	keys := map[string]struct{}{}
	for _, item := range items {
		if backend, key := keyOf(item); backend == server {
			keys[key] = struct{}{}
		}
	}
	return keys, nil
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
//   - on the Legacy session of that call, while the call's request is open: a
//     notifications/cancelled for the call's ID, and a JSON-RPC response
//     (such as an elicitation reply).
//
// A token with a list-call entry may send the handshake, server/discover, ping,
// GET and DELETE as above, and the list method its entry names, any number of
// times, each answered with only the items of the entry's server (see
// serveListCall). It starts no call, so it is never used up.
//
// Everything else is refused (see refuse), and everything after the token's
// exp with 401. A later refusal, such as the session-ownership 404 or a Cedar
// 403, still uses up the token: the client asks for a new token rather than
// retrying. So does a retried tools/call, such as the retry after a URL
// elicitation: each attempt needs its own token.
func (s *Server) callBindingMiddleware(next http.Handler) http.Handler {
	cb := s.callBinding
	if cb == nil {
		// Serve always sets it; a zero Config cannot fail validation.
		cb, _ = newCallBinding(callbinding.Config{}, s.callBindingRoute, s.callBindingListItems)
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
		token, reason := callbinding.FromClaims(identity.Claims, cb.required)
		if reason != "" {
			cb.refuse(w, r, parsed, &callbinding.Token{ClientID: callbinding.ClientID(identity.Claims)}, reason)
			return
		}
		if token == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != endpointPath {
			cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
			return
		}
		cb.serveBound(w, r, next, identity, token, parsed)
	})
}

// warnIfNoClient logs once when clients are listed and a token names its
// client in neither claim callbinding.ClientID reads, so such a token is
// never required to carry an entry.
func (cb *callBinding) warnIfNoClient(claims map[string]any) {
	if len(cb.required) == 0 || callbinding.ClientID(claims) != "" {
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
	if !cb.now().Before(token.ExpiresAt.Add(callBindingLeeway)) {
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenExpired)
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
	case token.Entry.IsList():
		cb.serveListCall(w, r, next, identity, token, parsed)
	case parsed.Method == "tools/list":
		// Read-only and filtered by the usual admission. MCP clients list
		// tools around a call, for example to read a tool's output schema.
		next.ServeHTTP(w, r)
	case parsed.Method == "tools/call":
		cb.serveToolCall(w, r, next, identity, token, parsed)
	case parsed.Method == "notifications/cancelled":
		requestID, ok := cancelledRequestID(parsed.Params)
		if !ok {
			cb.refuse(w, r, parsed, token, callbinding.ReasonNoOpenCall)
			return
		}
		cb.serveCallMessage(w, r, next, token, parsed, requestID)
	default:
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
	}
}

// serveListCall handles a request, other than the handshake, server/discover
// and ping, sent with a list-call token. It admits only the entry's list
// method and answers it with only the items of the entry's server: a list
// request names no server, so the server binding is applied to the answer.
// A tools/call or another list method is refused with method_mismatch, and
// any other request with method_not_allowed.
//
// The list is served by the usual handler, after every later check, and then
// filtered here, on the response: a Legacy list is answered by the SDK from
// the session's own tool store, which toolhive-core's mcpcompat offers no hook
// to filter, so this is the one place that covers Legacy and Modern alike.
// Every response body is filtered, whatever its status, so nothing that
// carries a result goes out unfiltered; only an empty body (such as the 202
// for a notification), a JSON-RPC error, and a body that is not JSON on a
// status other than 2xx (such as the session-ownership 404) pass unchanged.
// Anything else gets a 500.
//
// A list call starts no call and does not use up the token, so a client may
// follow nextCursor with the same token.
func (cb *callBinding) serveListCall(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	switch {
	case parsed.Method == token.Entry.Method:
	case parsed.Method == "tools/call" || toolcalldetails.IsListMethod(parsed.Method):
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodMismatch)
		return
	default:
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
		return
	}
	keep, err := cb.listItems(r.Context(), identity, parsed.Method, token.Entry.Server)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "list", err)
		return
	}
	buffered := &listResponseBuffer{header: w.Header(), status: http.StatusOK}
	next.ServeHTTP(buffered, r)

	body := buffered.body.Bytes()
	if len(body) == 0 && r.Context().Err() != nil {
		// The client went away before the handler answered: nothing to send.
		slog.DebugContext(r.Context(), "call binding: a list call ended before it was answered", "jti", token.JTI)
		return
	}
	if len(body) > 0 {
		filtered, err := callbinding.FilterListResult(parsed.Method, body, keep)
		switch {
		case err == nil:
			body = filtered
		case buffered.status >= http.StatusMultipleChoices && !json.Valid(body):
			// A plain error body, such as the session-ownership 404.
		default:
			slog.ErrorContext(r.Context(), "call binding: could not limit a list result to the token's server",
				"jti", token.JTI, "mcp_method", parsed.Method, "status", buffered.status, "error", err)
			w.Header().Del("Content-Length")
			writeCallBindingError(w, parsed, http.StatusInternalServerError, mcpparser.CodeInternalError,
				"Internal error: the list could not be limited to the access token's server")
			return
		}
	}
	slog.DebugContext(r.Context(), "call binding: answered a call-bound list call",
		"jti", token.JTI, "client_id", token.ClientID, "server", token.Entry.Server, "method", token.Entry.Method)
	w.Header().Del("Content-Length")
	w.WriteHeader(buffered.status)
	//nolint:gosec // G705: a JSON-RPC response, not rendered as HTML
	_, _ = w.Write(body)
}

// listResponseBuffer holds the response to a list call until serveListCall has
// filtered it. Headers go to the real writer's header map; the status and
// body are held. Flush is a no-op, and it has no Unwrap, so nothing reaches the
// real writer before the filter runs.
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
	_, started, err := cb.record.Lookup(r.Context(), token.Key)
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
	call, started, err := cb.record.Lookup(r.Context(), token.Key)
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
		cb.refuse(w, r, parsed, token, reason)
		return
	}

	now := cb.now()
	expiry := cb.recordExpiry(token, now)
	call := callbinding.Call{SessionID: callSessionID(r, parsed), RequestID: requestID, Started: now}
	started, err := cb.record.Start(ctx, token.Key, call, expiry)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	}
	if !started {
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenReused)
		return
	}
	defer cb.endCall(ctx, token, expiry)
	slog.DebugContext(ctx, "call binding: admitted a call-bound tools/call",
		"jti", token.JTI, "client_id", token.ClientID,
		"operation_id", token.Entry.OperationID, "server", token.Entry.Server, "tool", token.Entry.Tool)
	// The call's request is open while this runs. On Modern, and on Legacy
	// while the client stays connected, that is the whole call. A Legacy
	// client that closes the POST before it cancels gets no_open_call for the
	// cancellation: accepting messages for a call after its request returns
	// is left to the change that also accepts them after exp.
	next.ServeHTTP(w, r)
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

// callSessionID is the session a tools/call runs on: the Mcp-Session-Id of a
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

// endCall marks the call the token started as ended once its request has
// returned. The request context may already be cancelled, so the record gets
// a context of its own with a deadline: a stalled store must not hold the
// handler, and with it Shutdown. When the record cannot mark the call, this
// replica remembers it, so it refuses the call's messages from then on.
func (cb *callBinding) endCall(ctx context.Context, token *callbinding.Token, expiry time.Time) {
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callBindingEndTimeout)
	defer cancel()
	if err := cb.record.End(endCtx, token.Key); err != nil {
		cb.returned.add(token.Key, expiry, cb.now())
		slog.WarnContext(ctx, "call binding: could not mark a call as ended; "+
			"this replica refuses its messages, other replicas follow the record",
			"jti", token.JTI, "error", err)
	}
}

// recordExpiry is how long the record keeps a token started at now: until the
// token can no longer be accepted, or until the call has run for the maximum
// call duration, whichever is later, plus callBindingRetentionMargin.
func (cb *callBinding) recordExpiry(token *callbinding.Token, now time.Time) time.Time {
	expiry := token.ExpiresAt.Add(callBindingLeeway)
	if callEnd := now.Add(cb.maxCallDuration); callEnd.After(expiry) {
		expiry = callEnd
	}
	return expiry.Add(callBindingRetentionMargin)
}

// serveCallMessage admits a message that belongs to the call the token
// started, on that call's session, while the call's request is open and
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
	call, found, err := cb.record.Lookup(r.Context(), token.Key)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	}
	if !found || !cb.callOpen(token.Key, call) || !belongsToCall(r, call, requestID) {
		cb.refuse(w, r, parsed, token, callbinding.ReasonNoOpenCall)
		return
	}
	next.ServeHTTP(w, r)
}

// callOpen reports whether the call recorded under key still has its request
// open and is younger than the maximum call duration.
func (cb *callBinding) callOpen(key string, call callbinding.Call) bool {
	now := cb.now()
	return !call.Ended && !cb.returned.has(key, now) && now.Before(call.Started.Add(cb.maxCallDuration))
}

// belongsToCall reports whether a message sent as r is on call's session and,
// for a cancellation (requestID non-empty), names call's request.
func belongsToCall(r *http.Request, call callbinding.Call, requestID string) bool {
	sessionID, err := sessionbinding.RequestID(r, "")
	return err == nil && call.SessionID != "" && sessionID == call.SessionID &&
		(requestID == "" || requestID == call.RequestID)
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
		cb.challenge.WriteInvalidToken(w, "the access token is not accepted ("+reason+")")
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

// returnedCalls is the set of calls, keyed by token key, whose request has
// returned on this replica but that the record could not mark as ended. Each
// entry lasts as long as the record keeps its key. Entries are added only
// when the record fails, and expired ones are dropped on each add.
type returnedCalls struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func (c *returnedCalls) add(key string, expiry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expires == nil {
		c.expires = make(map[string]time.Time)
	}
	maps.DeleteFunc(c.expires, func(_ string, e time.Time) bool { return !now.Before(e) })
	c.expires[key] = expiry
}

func (c *returnedCalls) has(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.expires[key]
	return ok && now.Before(expiry)
}
