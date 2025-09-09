// Package streamable provides a streamable HTTP proxy for MCP servers.
package streamable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const (
	// StreamableHTTPEndpoint is the endpoint for streamable HTTP.
	StreamableHTTPEndpoint = "/mcp"

	// Default timeouts and buffer sizes
	defaultResponseTimeout = 30 * time.Second
)

// HTTPProxy implements a proxy for streamable HTTP transport.
type HTTPProxy struct {
	host              string
	port              int
	containerName     string
	shutdownCh        chan struct{}
	prometheusHandler http.Handler
	middlewares       []types.MiddlewareFunction

	// Message channel for sending JSON-RPC to the container (from HTTP -> runner)
	messageCh chan jsonrpc2.Message
	// Response channel for receiving JSON-RPC from the container (runner -> HTTP)
	responseCh chan jsonrpc2.Message

	// Session manager for streamable HTTP sessions
	sessionManager *session.Manager

	// Waiters keyed by JSON-encoded request ID -> one-shot channel for response delivery
	waiters sync.Map // map[string]chan jsonrpc2.Message
	// Map of compositeKey(sessID|idKey) -> original client JSON-RPC ID to restore before replying
	idRestore sync.Map // map[string]jsonrpc2.ID

	server   *http.Server
	stopOnce sync.Once
}

// NewHTTPProxy creates a new HTTPProxy for streamable HTTP transport.
func NewHTTPProxy(
	host string,
	port int,
	containerName string,
	prometheusHandler http.Handler,
	middlewares ...types.MiddlewareFunction,
) *HTTPProxy {
	// Use typed Streamable sessions
	sFactory := func(id string) session.Session { return session.NewStreamableSession(id) }

	return &HTTPProxy{
		host:              host,
		port:              port,
		containerName:     containerName,
		shutdownCh:        make(chan struct{}),
		prometheusHandler: prometheusHandler,
		middlewares:       middlewares,
		messageCh:         make(chan jsonrpc2.Message, 100),
		responseCh:        make(chan jsonrpc2.Message, 100),
		sessionManager:    session.NewManager(session.DefaultSessionTTL, sFactory),
	}
}

// Start starts the HTTPProxy server.
func (p *HTTPProxy) Start(_ context.Context) error {
	mux := http.NewServeMux()
	mux.Handle(StreamableHTTPEndpoint, p.applyMiddlewares(http.HandlerFunc(p.handleStreamableRequest)))

	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
	}

	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Route container responses to matching waiter channels
	go p.dispatchResponses()

	go func() {
		logger.Infof("Streamable HTTP proxy started for container %s on port %d", p.containerName, p.port)
		logger.Infof("Streamable HTTP endpoint: http://%s:%d%s", p.host, p.port, StreamableHTTPEndpoint)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Streamable HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTPProxy server.
func (p *HTTPProxy) Stop(ctx context.Context) error {
	var err error

	p.stopOnce.Do(func() {
		close(p.shutdownCh)

		// Stop session manager cleanup and disconnect sessions
		if p.sessionManager != nil {
			p.sessionManager.Stop()
			p.sessionManager.Range(func(_, value interface{}) bool {
				if ss, ok := value.(*session.StreamableSession); ok {
					ss.Disconnect()
				}
				return true
			})
		}

		if p.server != nil {
			if e := p.server.Shutdown(ctx); e != nil {
				err = e
			}
		}
	})

	return err
}

// GetMessageChannel returns the message channel for sending JSON-RPC to the container.
func (p *HTTPProxy) GetMessageChannel() chan jsonrpc2.Message {
	return p.messageCh
}

// GetResponseChannel returns the response channel for receiving JSON-RPC from the container.
func (p *HTTPProxy) GetResponseChannel() <-chan jsonrpc2.Message {
	return p.responseCh
}

// SendMessageToDestination sends a message to the container.
func (p *HTTPProxy) SendMessageToDestination(msg jsonrpc2.Message) error {
	select {
	case p.messageCh <- msg:
		return nil
	default:
		return fmt.Errorf("failed to send message to destination")
	}
}

// ForwardResponseToClients forwards a response from the container to the client.
func (p *HTTPProxy) ForwardResponseToClients(_ context.Context, msg jsonrpc2.Message) error {
	select {
	case p.responseCh <- msg:
		return nil
	default:
		return fmt.Errorf("failed to forward response to client")
	}
}

// SendResponseMessage is for compatibility with the Proxy interface.
func (p *HTTPProxy) SendResponseMessage(msg jsonrpc2.Message) error {
	return p.ForwardResponseToClients(context.Background(), msg)
}

// ------------------------- HTTP handlers -------------------------

// handleStreamableRequest handles HTTP POST requests to /mcp.
func (p *HTTPProxy) handleStreamableRequest(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p.handleGet(w, r)
	case http.MethodDelete:
		p.handleDelete(w, r)
	case http.MethodPost:
		p.handlePost(w, r)
	default:
		writeHTTPError(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (*HTTPProxy) handleGet(w http.ResponseWriter, _ *http.Request) {
	// SSE not offered here; explicit 405 is spec-compliant
	writeHTTPError(w, http.StatusMethodNotAllowed, "SSE not supported on this endpoint")
}

func (p *HTTPProxy) handleDelete(w http.ResponseWriter, r *http.Request) {
	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" {
		writeHTTPError(w, http.StatusBadRequest, "Mcp-Session-Id header required for DELETE")
		return
	}
	if _, ok := p.sessionManager.Get(sessID); !ok {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return
	}
	p.sessionManager.Delete(sessID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *HTTPProxy) handlePost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Optionally validate MCP-Protocol-Version; accept missing for compatibility
	protoVer := r.Header.Get("MCP-Protocol-Version")
	if protoVer != "" && !isSupportedMCPVersion(protoVer) {
		writeHTTPError(w, http.StatusBadRequest, "Unsupported MCP-Protocol-Version")
		return
	}

	// Read request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Error reading request body: %v", err))
		return
	}

	// Batch vs single message
	if isBatch(body) {
		sessID, err := p.resolveSessionForBatch(w, r)
		if err != nil {
			return
		}
		p.handleBatchRequest(w, body, sessID)
		return
	}

	msg, ok := decodeJSONRPCMessage(w, body)
	if !ok {
		return
	}

	// Notifications or client responses are accepted and forwarded (202)
	if p.handleNotificationOrClientResponse(w, msg) {
		return
	}

	req, ok := msg.(*jsonrpc2.Request)
	if !ok || !req.ID.IsValid() {
		writeHTTPError(w, http.StatusBadRequest, "Invalid JSON-RPC request (missing id)")
		return
	}

	// Resolve session per spec (initialize vs ordinary)
	sessID, setSessionHeader, err := p.resolveSessionForRequest(w, r, req)
	if err != nil {
		return
	}

	// If client accepts SSE, stream the response on an SSE stream for this request
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		p.handleSingleRequestSSE(ctx, w, sessID, req, setSessionHeader)
		return
	}

	// Request/response path with correlation (JSON response)
	p.handleSingleRequest(ctx, w, sessID, req, setSessionHeader)
}

// handleBatchRequest processes a batch JSON-RPC request and writes a batch response.
func (p *HTTPProxy) handleBatchRequest(w http.ResponseWriter, body []byte, sessID string) {
	rawMessages, ok := decodeBatch(w, body)
	if !ok {
		return
	}

	var responses []json.RawMessage
	hadRequest := false

	for _, raw := range rawMessages {
		// Detect if this element is a request with an ID
		if msg, err := jsonrpc2.DecodeMessage(raw); err == nil {
			if req, ok := msg.(*jsonrpc2.Request); ok && req.ID.IsValid() {
				hadRequest = true
			}
		}
		resp := p.processSingleMessage(sessID, raw)
		if resp != nil {
			responses = append(responses, resp)
		}
	}

	if !hadRequest {
		// Per spec: batches containing only notifications/responses -> 202 Accepted, no body
		w.WriteHeader(http.StatusAccepted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	// It's valid to return an empty array if requests produced no responses
	if err := json.NewEncoder(w).Encode(responses); err != nil {
		logger.Errorf("Failed to encode batch response: %v", err)
	}
}

// handleSingleRequest handles a single JSON-RPC request message end-to-end.
func (p *HTTPProxy) handleSingleRequest(
	ctx context.Context,
	w http.ResponseWriter,
	sessID string,
	req *jsonrpc2.Request,
	setSessionHeader bool,
) {
	ctx, cancel := context.WithTimeout(ctx, defaultResponseTimeout)
	defer cancel()

	msg, err := p.doRequest(ctx, sessID, req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			logger.Warnf("Timeout waiting for response for method=%s", req.Method)
			writeHTTPError(w, http.StatusGatewayTimeout, "Timeout waiting for response from container")
		} else {
			logger.Errorf("Failed to process request method=%s: %v", req.Method, err)
			writeHTTPError(w, http.StatusInternalServerError, "Failed to process request")
		}
		return
	}

	if setSessionHeader {
		w.Header().Set("Mcp-Session-Id", sessID)
	}
	if err := writeJSONRPC(w, msg); err != nil {
		logger.Errorf("Failed to write JSON-RPC response: %v", err)
	}
}

func (p *HTTPProxy) handleSingleRequestSSE(
	ctx context.Context,
	w http.ResponseWriter,
	sessID string,
	req *jsonrpc2.Request,
	setSessionHeader bool,
) {
	ctx, cancel := context.WithTimeout(ctx, defaultResponseTimeout)
	defer cancel()

	// Prepare SSE response headers
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeHTTPError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if setSessionHeader {
		w.Header().Set("Mcp-Session-Id", sessID)
	}
	flusher.Flush()

	msg, err := p.doRequest(ctx, sessID, req)
	if err != nil {
		// Send a best-effort error event
		errMsg := "Internal error"
		code := -32603
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			errMsg = "Timeout"
			code = -32000
		}
		errObj := map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID.Raw(),
			"error": map[string]any{
				"code":    code,
				"message": errMsg,
			},
		}
		if data, mErr := json.Marshal(errObj); mErr == nil {
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
		return
	}

	data, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		logger.Errorf("Failed to encode JSON-RPC response: %v", err)
		writeHTTPError(w, http.StatusInternalServerError, "Failed to encode response")
		return
	}
	// Write SSE event with the JSON-RPC response and flush
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

// processSingleMessage processes one raw JSON-RPC in a batch and returns encoded response bytes or nil.
func (p *HTTPProxy) processSingleMessage(sessID string, raw json.RawMessage) json.RawMessage {
	// Note: batch processing path
	msg, err := jsonrpc2.DecodeMessage(raw)
	if err != nil {
		logger.Warnf("Skipping invalid message in batch: %s", string(raw))
		return nil
	}

	// Notifications: just forward and continue
	if isNotification(msg) {
		if err := p.SendMessageToDestination(msg); err != nil {
			logger.Errorf("Failed to send notification to destination: %v", err)
		}
		return nil
	}

	// Client responses: accept and forward, no HTTP body
	if _, ok := msg.(*jsonrpc2.Response); ok {
		if err := p.SendMessageToDestination(msg); err != nil {
			logger.Errorf("Failed to forward client response to destination: %v", err)
		}
		return nil
	}

	req, ok := msg.(*jsonrpc2.Request)
	if !ok || !req.ID.IsValid() {
		logger.Warnf("Skipping invalid batch item (not a request with ID/response/notification): %T", msg)
		return nil
	}

	waitCh, cleanup := p.createWaiter(sessID, req.ID)
	defer cleanup()
	bkey := idKeyFromID(req.ID)

	// Transform outgoing request ID to composite and send
	ck := compositeKey(sessID, bkey)
	proxiedMsg, err := encodeRequestWithID(req, ck)
	if err != nil {
		logger.Errorf("Failed to encode batch request: %v", err)
		return nil
	}
	if err := p.SendMessageToDestination(proxiedMsg); err != nil {
		logger.Errorf("Failed to send message to destination: %v", err)
		return nil
	}

	response := p.waitForResponse(waitCh, defaultResponseTimeout)
	if response == nil {
		logger.Warnf("StreamableHTTP: batch timeout waiting for key=%s", bkey)
		return nil
	}

	if r, ok := response.(*jsonrpc2.Response); ok && r.ID.IsValid() {
		restored, err := p.restoreResponseID(r, ck)
		if err != nil {
			logger.Errorf("Failed to restore response ID: %v", err)
			return nil
		}
		data, err := jsonrpc2.EncodeMessage(restored)
		if err != nil {
			logger.Errorf("Failed to encode JSON-RPC response: %v", err)
			return nil
		}
		return data
	}

	logger.Warnf("Received invalid message that is not a valid response: %T", response)
	return nil
}

func encodeRequestWithID(req *jsonrpc2.Request, newID string) (jsonrpc2.Message, error) {
	data, err := jsonrpc2.EncodeMessage(req)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["id"] = newID
	data2, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return jsonrpc2.DecodeMessage(data2)
}

func (p *HTTPProxy) restoreResponseID(resp *jsonrpc2.Response, ck string) (jsonrpc2.Message, error) {
	orig, ok := p.idRestore.Load(ck)
	if !ok {
		// No restore information; return as-is
		return resp, nil
	}
	origID, _ := orig.(jsonrpc2.ID)

	data, err := jsonrpc2.EncodeMessage(resp)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["id"] = origID.Raw()
	data2, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return jsonrpc2.DecodeMessage(data2)
}

func (p *HTTPProxy) doRequest(ctx context.Context, sessID string, req *jsonrpc2.Request) (jsonrpc2.Message, error) {
	key := idKeyFromID(req.ID)
	ck := compositeKey(sessID, key)

	waitCh, cleanup := p.createWaiter(sessID, req.ID)
	defer cleanup()

	proxiedMsg, err := encodeRequestWithID(req, ck)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	if err := p.SendMessageToDestination(proxiedMsg); err != nil {
		return nil, fmt.Errorf("send message: %w", err)
	}

	select {
	case msg := <-waitCh:
		if r, ok := msg.(*jsonrpc2.Response); ok && r.ID.IsValid() {
			restored, err := p.restoreResponseID(r, ck)
			if err != nil {
				return nil, fmt.Errorf("restore id: %w", err)
			}
			return restored, nil
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.shutdownCh:
		return nil, context.Canceled
	}
}

// ------------------------- Helpers: middleware, parsing, correlation -------------------------

func (p *HTTPProxy) applyMiddlewares(handler http.Handler) http.Handler {
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		handler = p.middlewares[i](handler)
	}
	return handler
}

func (p *HTTPProxy) ensureSession(id string) error {
	if _, ok := p.sessionManager.Get(id); ok {
		return nil
	}
	return p.sessionManager.AddWithID(id)
}

// resolveSessionForBatch resolves or creates an ephemeral session for batch POSTs.
// Writes appropriate HTTP errors and returns an error when handling should stop.
func (p *HTTPProxy) resolveSessionForBatch(w http.ResponseWriter, r *http.Request) (string, error) {
	sessID := r.Header.Get("Mcp-Session-Id")
	if sessID == "" {
		sessID = uuid.New().String()
		if err := p.ensureSession(sessID); err != nil {
			writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create session: %v", err))
			return "", err
		}
		return sessID, nil
	}
	if _, ok := p.sessionManager.Get(sessID); !ok {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return "", fmt.Errorf("session not found")
	}
	return sessID, nil
}

// resolveSessionForRequest resolves session rules for a single JSON-RPC request.
// On initialize, assigns session if missing and returns setSessionHeader=true.
// For other methods, allows optional session by creating ephemeral (no header set).
// Writes HTTP errors on failure and returns error to stop handling.
func (p *HTTPProxy) resolveSessionForRequest(
	w http.ResponseWriter,
	r *http.Request,
	req *jsonrpc2.Request,
) (string, bool, error) {
	var setSessionHeader bool
	sessID := r.Header.Get("Mcp-Session-Id")

	if req.Method == "initialize" {
		if sessID == "" {
			sessID = uuid.New().String()
			setSessionHeader = true
		}
		if err := p.ensureSession(sessID); err != nil {
			writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create session: %v", err))
			return "", false, err
		}
		return sessID, setSessionHeader, nil
	}

	// Non-initialize path: sessions are optional; create ephemeral if missing
	if sessID == "" {
		sessID = uuid.New().String()
		if err := p.ensureSession(sessID); err != nil {
			writeHTTPError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to create session: %v", err))
			return "", false, err
		}
		return sessID, false, nil
	}

	// If session is provided, ensure it exists
	if _, ok := p.sessionManager.Get(sessID); !ok {
		writeHTTPError(w, http.StatusNotFound, "session not found")
		return "", false, fmt.Errorf("session not found")
	}
	return sessID, false, nil
}

func isBatch(body []byte) bool {
	t := bytes.TrimSpace(body)
	return len(t) > 0 && t[0] == '['
}

func decodeBatch(w http.ResponseWriter, body []byte) ([]json.RawMessage, bool) {
	var rawMessages []json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(body), &rawMessages); err != nil {
		logger.Warnf("Failed to decode batch JSON-RPC: %s", string(body))
		writeHTTPError(w, http.StatusBadRequest, "Invalid batch JSON-RPC")
		return nil, false
	}
	return rawMessages, true
}

// decodeJSONRPCMessage decodes a JSON-RPC message from the request body.
func decodeJSONRPCMessage(w http.ResponseWriter, body []byte) (jsonrpc2.Message, bool) {
	msg, err := jsonrpc2.DecodeMessage(body)
	if err != nil {
		logger.Warnf("Skipping message that failed to decode: %s", string(body))
		writeHTTPError(w, http.StatusBadRequest, "Invalid JSON-RPC 2.0 message")
		return nil, false
	}
	return msg, true
}

func (p *HTTPProxy) handleNotificationOrClientResponse(w http.ResponseWriter, msg jsonrpc2.Message) bool {
	if isNotification(msg) || (func() bool { _, ok := msg.(*jsonrpc2.Response); return ok })() {
		if err := p.SendMessageToDestination(msg); err != nil {
			logger.Errorf("Failed to send message to destination: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
		return true
	}
	return false
}

// createWaiter registers a waiter channel for the given request ID and returns cleanup fn.
func (p *HTTPProxy) createWaiter(sessID string, id jsonrpc2.ID) (chan jsonrpc2.Message, func()) {
	key := idKeyFromID(id)
	ck := compositeKey(sessID, key)
	// store original client id to restore before replying
	p.idRestore.Store(ck, id)

	ch := make(chan jsonrpc2.Message, 1)
	p.waiters.Store(ck, ch)

	cleanup := func() {
		p.waiters.Delete(ck)
		p.idRestore.Delete(ck)
	}
	return ch, cleanup
}
