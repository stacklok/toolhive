// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
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

// Methods a call-bound token may send without starting a call: the Legacy
// handshake, the Modern capability probe and ping.
var callBindingPassMethods = []string{
	"initialize", "notifications/initialized", "ping", methodServerDiscover,
}

// callBindingRoute resolves a tools/call name to the backend it is routed to
// for identity, or to "" when the tool has no single backend.
type callBindingRoute func(ctx context.Context, identity *auth.Identity, name string) (string, error)

// callBinding enforces access tokens that carry an RFC 9396
// authorization_details entry of type https://toolhive.dev/mcp_tool_call (see
// pkg/mcp/toolcalldetails). Such a token may start one tools/call, the one its
// entry names, and only once.
type callBinding struct {
	required        map[string]struct{}
	maxCallDuration time.Duration
	record          callbinding.Record
	route           callBindingRoute
	now             func() time.Time
}

// newCallBinding validates cfg and builds the enforcement it describes.
func newCallBinding(cfg callbinding.Config, route callBindingRoute) (*callBinding, error) {
	if cfg.MaxCallDuration < 0 {
		return nil, fmt.Errorf("%w: CallBinding.MaxCallDuration must not be negative", vmcp.ErrInvalidConfig)
	}
	if slices.Contains(cfg.Clients, "") {
		return nil, fmt.Errorf("%w: CallBinding.Clients must not contain an empty client ID", vmcp.ErrInvalidConfig)
	}
	cb := &callBinding{
		required:        make(map[string]struct{}, len(cfg.Clients)),
		maxCallDuration: cfg.MaxCallDuration,
		record:          cfg.Record,
		route:           route,
		now:             time.Now,
	}
	for _, client := range cfg.Clients {
		cb.required[client] = struct{}{}
	}
	if cb.maxCallDuration == 0 {
		cb.maxCallDuration = callbinding.DefaultMaxCallDuration
	}
	if cb.record == nil {
		cb.record = callbinding.NewMemoryRecord()
	}
	return cb, nil
}

// callBindingRoute resolves name the way this instance dispatches a
// tools/call. With the optimizer enabled, a Legacy session's only tools are
// the optimizer's meta-tools (serve_optimizer.go) and Modern is refused
// (modern_gate.go), so no name reaches a single backend. Otherwise the core
// routes every tools/call, on both revisions, and a tool's BackendID names its
// backend; composite tools and code mode's execute_tool_script have none.
// The decision follows what a name resolves to, so a new kind of tool that
// dispatches further calls is refused as long as it has no BackendID.
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

// callBindingMiddleware enforces call-bound access tokens. It runs after
// authentication and parsing and before classification, so it covers Legacy
// and Modern requests alike, and before session ownership, the authorization
// CallGate, Cedar and dispatch. It can only refuse: a request it passes goes
// on through every later check.
//
// A token without an mcp_tool_call entry passes unchanged, unless its client
// is listed in CallBinding.Clients. A call-bound token may send:
//   - the handshake (initialize, notifications/initialized), the Modern
//     server/discover probe, and ping;
//   - GET and DELETE, which the session-ownership check then limits to the
//     token's own session; neither uses up the token;
//   - one tools/call that matches its entry, which uses up the token;
//   - a notifications/cancelled for that call, and a JSON-RPC response (such
//     as an elicitation reply) on the call's session, while the call is open.
//
// Everything else, and everything after the token's exp, is refused with 403.
// A later refusal, such as the session-ownership 404 or a Cedar 403, still
// uses up the token: the client asks for a new token rather than retrying.
func (s *Server) callBindingMiddleware(next http.Handler) http.Handler {
	cb := s.callBinding
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, _ := auth.IdentityFromContext(r.Context())
		if identity == nil {
			next.ServeHTTP(w, r)
			return
		}
		parsed := mcpparser.GetParsedMCPRequest(r.Context())
		token, reason := callbinding.FromClaims(identity.Claims, cb.required)
		if reason != "" {
			clientID, _ := identity.Claims["client_id"].(string)
			cb.refuse(w, r, parsed, &callbinding.Token{ClientID: clientID}, reason)
			return
		}
		if token == nil {
			next.ServeHTTP(w, r)
			return
		}
		cb.serveBound(w, r, next, identity, token, parsed)
	})
}

// serveBound handles a request that carries a call-bound token.
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
		next.ServeHTTP(w, r)
		return
	case http.MethodPost:
	default:
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
		return
	}

	// ParsingMiddleware parses every POST and answers one it cannot parse, so
	// a POST without a parsed request is a JSON-RPC response.
	if parsed == nil {
		cb.serveOpenCallMessage(w, r, next, token, parsed, "")
		return
	}
	switch {
	case slices.Contains(callBindingPassMethods, parsed.Method):
		next.ServeHTTP(w, r)
	case token.Entry.IsList():
		cb.refuse(w, r, parsed, token, callbinding.ReasonListCallUnsupported)
	case parsed.Method == "tools/call":
		cb.serveToolCall(w, r, next, identity, token, parsed)
	case parsed.Method == "notifications/cancelled":
		requestID, ok := callbinding.CancelledRequestID(parsed.Params)
		if !ok {
			cb.refuse(w, r, parsed, token, callbinding.ReasonNoOpenCall)
			return
		}
		cb.serveOpenCallMessage(w, r, next, token, parsed, requestID)
	default:
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
	}
}

// serveToolCall admits the one tools/call the token's entry names and records
// the token as used.
func (cb *callBinding) serveToolCall(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	identity *auth.Identity, token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest,
) {
	ctx := r.Context()
	requestID, ok := callbinding.RequestID(parsed.ID)
	if !ok {
		// A tools/call notification has no ID and starts no call the client
		// could see; nothing legitimate sends one.
		cb.refuse(w, r, parsed, token, callbinding.ReasonMethodNotAllowed)
		return
	}
	// Read params as received, before routing, prefix stripping or any
	// rewrite, so the hash covers what the client sent.
	name, arguments, err := callbinding.ToolCallParams(parsed.Params)
	if err != nil {
		cb.refuse(w, r, parsed, token, callbinding.ReasonAmbiguousParams)
		return
	}
	if name != token.Entry.Tool {
		cb.refuse(w, r, parsed, token, callbinding.ReasonToolMismatch)
		return
	}
	hash, err := callbinding.ArgumentsSHA256(arguments)
	if err != nil {
		cb.refuse(w, r, parsed, token, callbinding.ReasonInvalidArguments)
		return
	}
	if hash != token.Entry.ArgumentsSHA256 {
		cb.refuse(w, r, parsed, token, callbinding.ReasonArgumentsMismatch)
		return
	}
	backendID, err := cb.route(ctx, identity, name)
	switch {
	case errors.Is(err, vmcp.ErrNotFound):
		// Unknown, unadvertised, or hidden from this caller by Cedar.
		cb.refuse(w, r, parsed, token, callbinding.ReasonToolNotFound)
		return
	case err != nil:
		cb.unavailable(w, r, parsed, token, "route", err)
		return
	case backendID == "":
		cb.refuse(w, r, parsed, token, callbinding.ReasonToolNotBindable)
		return
	case backendID != token.Entry.Server:
		cb.refuse(w, r, parsed, token, callbinding.ReasonServerMismatch)
		return
	}

	now := cb.now()
	call := callbinding.Call{
		// The session-ownership check that follows refuses a session the
		// caller does not own, so the call is recorded on the session the
		// header names.
		SessionID: r.Header.Get("Mcp-Session-Id"),
		RequestID: requestID,
		Started:   now,
	}
	expiresAt := token.ExpiresAt.Add(callBindingLeeway)
	if callEnd := now.Add(cb.maxCallDuration); callEnd.After(expiresAt) {
		expiresAt = callEnd
	}
	started, err := cb.record.Start(ctx, token.Key, call, expiresAt)
	if err != nil {
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	}
	if !started {
		cb.refuse(w, r, parsed, token, callbinding.ReasonTokenReused)
		return
	}
	defer func() {
		// The request context may already be cancelled; ending the call must
		// still reach the record.
		if err := cb.record.End(context.WithoutCancel(ctx), token.Key); err != nil {
			slog.WarnContext(ctx, "call binding: could not mark a call as ended",
				"jti", token.JTI, "error", err)
		}
	}()
	slog.DebugContext(ctx, "call binding: admitted a call-bound tools/call",
		"jti", token.JTI, "client_id", token.ClientID,
		"operation_id", token.Entry.OperationID, "server", token.Entry.Server, "tool", token.Entry.Tool)
	// Both the Legacy streamable POST and the Modern dispatch return when the
	// call has completed, so the call is open exactly while this runs.
	next.ServeHTTP(w, r)
}

// serveOpenCallMessage admits a message that belongs to the call the token
// started: a notifications/cancelled for that call's ID (requestID non-empty)
// or a JSON-RPC response (requestID empty), sent on the call's session while
// the call is open and younger than the maximum call duration.
//
// Admitting any response on the call's session is a shortcut: the vMCP does
// not see which server-to-client requests the SDK is waiting on, so the
// session is the only binding (see "vMCP: bind the session to act", #6888).
func (cb *callBinding) serveOpenCallMessage(
	w http.ResponseWriter, r *http.Request, next http.Handler,
	token *callbinding.Token, parsed *mcpparser.ParsedMCPRequest, requestID string,
) {
	call, open, err := cb.record.OpenCall(r.Context(), token.Key)
	switch {
	case err != nil:
		cb.unavailable(w, r, parsed, token, "record", err)
		return
	case !open,
		!cb.now().Before(call.Started.Add(cb.maxCallDuration)),
		r.Header.Get("Mcp-Session-Id") != call.SessionID,
		requestID != "" && requestID != call.RequestID:
		cb.refuse(w, r, parsed, token, callbinding.ReasonNoOpenCall)
		return
	}
	next.ServeHTTP(w, r)
}

// refuse answers 403 with WWW-Authenticate: Bearer error="insufficient_scope"
// and a JSON-RPC error, and marks the request so the audit middleware records
// it as a denial with its reason. The challenge has no scope parameter: no
// scope would make the token acceptable, and clients read one as a step-up
// hint. The log carries the token's jti, never the token or the arguments.
func (*callBinding) refuse(
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
		"jti", token.JTI, "client_id", token.ClientID,
		"operation_id", token.Entry.OperationID, "server", token.Entry.Server, "tool", token.Entry.Tool)

	w.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope"`)
	writeCallBindingError(w, parsed, http.StatusForbidden, mcpparser.JSONRPCCodeDenied,
		"Forbidden: the access token does not permit this request ("+reason+")")
}

// unavailable answers 503 when the check cannot be completed. The token is
// not used up, so the client may retry with it.
func (*callBinding) unavailable(
	w http.ResponseWriter, r *http.Request, parsed *mcpparser.ParsedMCPRequest,
	token *callbinding.Token, step string, err error,
) {
	slog.WarnContext(r.Context(), "call binding: could not check a call-bound token",
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
	if parsed != nil && parsed.ID != nil {
		if id, err := mcpparser.ConvertToJSONRPC2ID(parsed.ID); err == nil {
			resp.ID = id
		}
	}
	//nolint:gosec // G104: writing a JSON-RPC error response to an HTTP client
	_ = mcpparser.WriteJSONRPCError(w, status, resp)
}
