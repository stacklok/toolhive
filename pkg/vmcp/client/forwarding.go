// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"log/slog"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/conversion"
)

// boundForwarders holds the server->client forwarding requesters bound onto the
// backend client after the SDK server is constructed. A nil field leaves that
// forwarding path disabled.
//
// Stored atomically on httpBackendClient so the client factory (which may run
// concurrently for different backends) reads a consistent snapshot without a
// mutex. Bound exactly once, before serving begins.
type boundForwarders struct {
	elicitation vmcp.ElicitationRequester
	sampling    vmcp.SamplingRequester
	notifier    vmcp.ClientNotifier
}

// BindForwarders implements vmcp.ClientForwarderBinder. server.New calls it once,
// after Serve builds the mcp-go server the adapters wrap and before serving
// begins, so the backend clients created per call can relay a backend's mid-call
// server->client traffic (elicitation, sampling, progress/logging) to the
// downstream client.
func (h *httpBackendClient) BindForwarders(
	elicitation vmcp.ElicitationRequester,
	sampling vmcp.SamplingRequester,
	notifier vmcp.ClientNotifier,
) {
	h.forwarders.Store(&boundForwarders{
		elicitation: elicitation,
		sampling:    sampling,
		notifier:    notifier,
	})
}

// enableBackendLogging requests debug-level logging from the backend so it emits
// notifications/message during a tool call, which the notification forwarder
// relays to the downstream client. It is a no-op when no forwarders are bound or
// the backend does not advertise the logging capability. Best-effort: a failure
// is logged at debug and does not fail the caller.
func (h *httpBackendClient) enableBackendLogging(
	ctx context.Context, c *client.Client, caps *mcp.ServerCapabilities, backendID string,
) {
	if h.forwarders.Load() == nil {
		return
	}
	if caps == nil || caps.Logging == nil {
		return
	}
	if err := c.SetLoggingLevel(ctx, mcp.LoggingLevelDebug); err != nil {
		slog.Debug("failed to set backend logging level", "backend", backendID, "error", err)
	}
}

// drainServerToClientNotifications flushes any in-flight backend->downstream
// notification off the per-call backend client before it is closed, closing a
// lost-notification race that otherwise drops fire-and-forget notifications
// (notifications/progress, notifications/message) under load.
//
// The race: a backend emits such a notification mid tools/call and then returns
// the result. The notification and the result travel on SEPARATE streams — the
// notification on the standalone SSE stream, the result on the tools/call
// response stream — and the client reads every stream through a single FIFO
// channel feeding one receive loop. CallTool returns as soon as the RESULT is
// read; its deferred Close then cancels the receive loop. If the standalone
// stream's notification has not yet been read and enqueued for handling when
// Close fires, it is discarded and never reaches newNotificationForwarder, so
// the downstream client never sees it (a permanent loss, not a slow delivery).
// Blocking server->client REQUESTS (elicitation/sampling) are immune: the
// backend tool blocks on the response, so the client is guaranteed to still be
// reading when they arrive.
//
// The fix is a synchronous ping used purely as a drain barrier. The backend
// flushes the notification onto the wire before returning the result, so by the
// time CallTool returns the notification bytes are already buffered on the
// client's standalone connection. A ping is a full backend round-trip; while it
// is in flight the receive loop drains the buffered notification off the shared
// FIFO channel (a local buffered read completes far ahead of the ping's network
// round-trip), enqueuing it for handling. Because the ping response arrives on
// that same FIFO channel AFTER the notification, the ping returning proves the
// notification was already read and enqueued. The subsequent Close then waits
// for enqueued handlers to finish (jsonrpc2.Connection.Close drains the handler
// queue), so the forward completes before teardown.
//
// Only runs when server->client forwarding is bound (fwd.notifier != nil, the
// same condition under which the notification forwarder is registered); other
// deployments keep the pre-forwarding fast path with no extra round-trip.
// Best-effort: a ping failure only forgoes the barrier — the tool result is
// already in hand, so it must not fail the call.
func (h *httpBackendClient) drainServerToClientNotifications(ctx context.Context, c *client.Client) {
	fwd := h.forwarders.Load()
	if fwd == nil || fwd.notifier == nil {
		return
	}
	if err := c.Ping(ctx); err != nil {
		slog.Debug("notification drain ping failed; forwarded notifications may be lost", "error", err)
	}
}

// forwardingClientOptions builds the client-level options that install the
// elicitation and sampling handlers on a backend client, each closing over the
// captured per-call downstream context so the handler can relay to the right
// session. Only the handlers whose requester is bound are installed. The handlers
// must be registered before Initialize, so these options are passed at
// construction (NewStreamableHttpClientWithOpts).
func forwardingClientOptions(callCtx context.Context, fwd *boundForwarders) []client.ClientOption {
	var opts []client.ClientOption
	if fwd.elicitation != nil {
		opts = append(opts, client.WithElicitationHandler(newElicitationForwarder(callCtx, fwd.elicitation)))
	}
	if fwd.sampling != nil {
		opts = append(opts, client.WithSamplingHandler(newSamplingForwarder(callCtx, fwd.sampling)))
	}
	return opts
}

// deriveForwardCtx derives a context from base (the captured per-call downstream
// session context, which the SDK adapters need to route server->client traffic
// to the right session) that is also cancelled when handler (the receive-loop
// context the go-sdk invokes the client handler with) is cancelled. This lets a
// forwarded elicitation/sampling round-trip honor a backend-side cancellation
// while still resolving the downstream session from base.
//
// The returned cancel func must be called to release the AfterFunc watcher.
func deriveForwardCtx(base, handler context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(base)
	stop := context.AfterFunc(handler, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// newElicitationForwarder builds a client elicitation handler that relays a
// backend's mid-call elicitation/create request to the downstream client via the
// bound requester, using the captured per-call downstream context (callCtx). The
// go-sdk invokes the handler on its receive loop with a context that does NOT
// carry the downstream session, so callCtx — not the handler's own ctx — is the
// one that resolves the session.
//
// When callCtx carries no downstream session (health probes, capability
// listing) the requester returns an error, which is relayed back to the backend
// as a clean elicitation failure rather than hanging.
func newElicitationForwarder(callCtx context.Context, req vmcp.ElicitationRequester) client.ElicitationHandlerFunc {
	return func(handlerCtx context.Context, r mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
		ctx, cancel := deriveForwardCtx(callCtx, handlerCtx)
		defer cancel()

		res, err := req.RequestElicitation(ctx, vmcp.ElicitationRequest{
			Message:         r.Params.Message,
			RequestedSchema: r.Params.RequestedSchema,
			Meta:            conversion.FromMCPMeta(r.Params.Meta),
		})
		if err != nil {
			return nil, err
		}
		return &mcp.ElicitationResult{
			ElicitationResponse: mcp.ElicitationResponse{
				Action:  mcp.ElicitationResponseAction(res.Action),
				Content: res.Content,
			},
		}, nil
	}
}

// newSamplingForwarder builds a client sampling handler that relays a backend's
// mid-call sampling/createMessage request to the downstream client via the bound
// requester, using the captured per-call downstream context. See
// newElicitationForwarder for the captured-context rationale.
func newSamplingForwarder(callCtx context.Context, req vmcp.SamplingRequester) client.SamplingHandlerFunc {
	return func(handlerCtx context.Context, r mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error) {
		ctx, cancel := deriveForwardCtx(callCtx, handlerCtx)
		defer cancel()

		res, err := req.RequestSampling(ctx, vmcp.SamplingRequest{
			Messages:         fromMCPSamplingMessages(r.Messages),
			ModelPreferences: fromMCPModelPreferences(r.ModelPreferences),
			SystemPrompt:     r.SystemPrompt,
			IncludeContext:   r.IncludeContext,
			Temperature:      r.Temperature,
			MaxTokens:        r.MaxTokens,
			StopSequences:    r.StopSequences,
			Metadata:         r.Metadata,
		})
		if err != nil {
			return nil, err
		}
		return &mcp.CreateMessageResult{
			SamplingMessage: mcp.SamplingMessage{
				Role:    mcp.Role(res.Role),
				Content: res.Content,
			},
			Model:      res.Model,
			StopReason: res.StopReason,
		}, nil
	}
}

// newNotificationForwarder builds an OnNotification handler that relays a
// backend's mid-call notifications/progress and notifications/message to the
// downstream client via the bound notifier, using the captured per-call
// downstream context. Forwarding is best-effort: a missing downstream session
// (the notifier no-ops) or a forwarding error is logged at debug and dropped,
// never surfaced to the backend. Other notification methods are ignored (the
// go-sdk server re-emits list-changed notifications automatically).
func newNotificationForwarder(callCtx context.Context, notifier vmcp.ClientNotifier) func(mcp.JSONRPCNotification) {
	// Backend notifications are delivered asynchronously and can arrive just after
	// the tool call context is cancelled; keep the captured downstream-session
	// values but ignore cancellation so best-effort forwarding still runs.
	forwardCtx := context.WithoutCancel(callCtx)

	return func(n mcp.JSONRPCNotification) {
		fields := n.Params.AdditionalFields
		switch n.Method {
		case vmcp.MethodProgressNotification:
			err := notifier.NotifyProgress(forwardCtx, vmcp.ProgressNotification{
				ProgressToken: fields["progressToken"],
				Progress:      toFloat(fields["progress"]),
				Total:         toFloat(fields["total"]),
				Message:       toString(fields["message"]),
			})
			if err != nil {
				slog.Debug("failed to forward progress notification", "error", err)
			}
		case vmcp.MethodLogNotification:
			err := notifier.NotifyLog(forwardCtx, vmcp.LogMessage{
				Level:  toString(fields["level"]),
				Logger: toString(fields["logger"]),
				Data:   fields["data"],
			})
			if err != nil {
				slog.Debug("failed to forward log notification", "error", err)
			}
		default:
			// Other notifications (list_changed, resources/updated, ...) are not
			// mid-call server->client traffic this forwarder relays.
		}
	}
}

// fromMCPSamplingMessages maps SDK sampling messages to the domain type.
func fromMCPSamplingMessages(msgs []mcp.SamplingMessage) []vmcp.SamplingMessage {
	if msgs == nil {
		return nil
	}
	out := make([]vmcp.SamplingMessage, len(msgs))
	for i, m := range msgs {
		out[i] = vmcp.SamplingMessage{
			Role:    string(m.Role),
			Content: m.Content,
		}
	}
	return out
}

// fromMCPModelPreferences maps the SDK model preferences to the domain type,
// preserving a nil (unset) value.
func fromMCPModelPreferences(p *mcp.ModelPreferences) *vmcp.ModelPreferences {
	if p == nil {
		return nil
	}
	hints := make([]vmcp.ModelHint, len(p.Hints))
	for i, h := range p.Hints {
		hints[i] = vmcp.ModelHint{Name: h.Name}
	}
	return &vmcp.ModelPreferences{
		Hints:                hints,
		CostPriority:         p.CostPriority,
		SpeedPriority:        p.SpeedPriority,
		IntelligencePriority: p.IntelligencePriority,
	}
}

// toFloat coerces a JSON-decoded numeric value to float64, returning 0 for a
// missing or non-numeric value.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// toString coerces a JSON-decoded value to string, returning "" when absent or
// not a string.
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
