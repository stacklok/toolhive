package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/logger"
)

// StdioBridge implements a bridge for MCP servers using stdio transport.
type StdioBridge struct {
	baseURL, postURL *url.URL
	headers          http.Header
	mode             string
	wg               sync.WaitGroup
	cancel           context.CancelFunc
}

// NewStdioBridge creates a new StdioBridge instance.
func NewStdioBridge(rawURL string) (*StdioBridge, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	return &StdioBridge{baseURL: u, postURL: u}, nil
}

// Start begins the transport loop for the StdioBridge.
func (b *StdioBridge) Start(ctx context.Context) {
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.loop(ctx)
}

func (b *StdioBridge) loop(ctx context.Context) {
	defer b.wg.Done()
	for {
		mode, err := b.detectTransport(ctx)
		if err != nil {
			logger.Errorf("transport detection failed: %v", err)
			return
		}
		b.mode = mode
		b.wg.Add(2)
		if mode == "streamable-http" {
			go b.runStreamableReader(ctx)
			go b.runStreamableWriter(ctx)
		} else { // legacy SSE
			b.postURL = b.baseURL // reset
			go b.runLegacyReader(ctx)
			go b.runLegacyWriter(ctx)
		}
		b.wg.Wait()
		select {
		case <-ctx.Done():
			return
		default:
			logger.Info("Session ended; restarting transport detection")
		}
	}
}

// Header include Mcp-Session-Id if needed
func (b *StdioBridge) detectTransport(ctx context.Context) (string, error) {
	// 1) Probe for legacy SSE without sending any JSON-RPC
	getReq, err := http.NewRequestWithContext(ctx, "GET", b.baseURL.String(), nil)
	if err != nil {
		return "", err
	}
	getReq.Header.Set("Accept", "text/event-stream")
	copyHeaders(getReq.Header, b.headers)

	getResp, err := http.DefaultClient.Do(getReq)
	if err == nil {
		ct := getResp.Header.Get("Content-Type")
		err = getResp.Body.Close() // we’re only peeking at headers here
		if err != nil {
			return "", fmt.Errorf("failed to close GET response body: %w", err)
		}
		if strings.HasPrefix(ct, "text/event-stream") {
			// Legacy SSE: runLegacyReader will open the real stream.
			return "legacy-sse", nil
		}
	}

	// 2) Not SSE -> treat as streamable HTTP and do a proper initialize
	body, err := buildJSONRPCInitializeRequest() // keep if your server expects initialize for streamable
	if err != nil {
		return "", err
	}
	postReq, err := http.NewRequestWithContext(ctx, "POST", b.baseURL.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	postReq.Header.Set("Content-Type", "application/json")
	postReq.Header.Set("Accept", "application/json, text/event-stream")
	copyHeaders(postReq.Header, b.headers)

	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		return "", err
	}
	defer postResp.Body.Close()

	// If a streamable server returns info + Mcp-Session-Id, great.
	// Some legacy gateways might reply 4xx to POST at base URL.
	if postResp.StatusCode >= 400 && postResp.StatusCode < 500 {
		return "legacy-sse", nil
	}

	// Streamable: capture session headers and emit the response payload.
	b.handleInitializeResponse(postResp)
	return "streamable-http", nil
}

func (b *StdioBridge) handleInitializeResponse(resp *http.Response) {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		if b.headers == nil {
			b.headers = make(http.Header)
		}
		b.headers.Set("Mcp-Session-Id", sid)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		logger.Errorf("failed to read response body: %v", err)
		return
	}
	payload := strings.TrimSpace(string(data))
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		emitJSON(payload)
	} else if strings.HasPrefix(ct, "text/event-stream") {
		parseSSE(bytes.NewReader(data))
	}
}

// Streamable HTTP handlers
func (b *StdioBridge) runStreamableReader(ctx context.Context) {
	defer b.wg.Done()
	req, err := http.NewRequestWithContext(ctx, "GET", b.baseURL.String(), nil)
	if err != nil {
		logger.Errorf("Failed to create GET request: %v", err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	copyHeaders(req.Header, b.headers)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Errorf("Streamable GET error: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		logger.Error("Session expired (404 on GET)")
		return
	}
	parseSSE(resp.Body)
}

func (b *StdioBridge) runStreamableWriter(ctx context.Context) {
	defer b.wg.Done()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 10*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var tmp json.RawMessage
		if err := json.Unmarshal([]byte(raw), &tmp); err != nil {
			logger.Errorf("Invalid JSON input: %v", err)
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "POST", b.baseURL.String(), strings.NewReader(raw))
		if err != nil {
			logger.Errorf("Failed to create HTTP request: %v", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		copyHeaders(req.Header, b.headers)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Errorf("POST error: %v", err)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			logger.Errorf("Error reading response body: %v", err)
			continue
		}
		payload := strings.TrimSpace(string(data))
		if resp.StatusCode == http.StatusNotFound {
			b.cancel()
			return
		}
		if strings.HasPrefix(ct, "application/json") {
			emitJSON(payload)
		} else if strings.HasPrefix(ct, "text/event-stream") {
			parseSSE(bytes.NewReader(data))
		}
	}
}

func (b *StdioBridge) runLegacyReader(ctx context.Context) {
	defer b.wg.Done()

	req, err := http.NewRequestWithContext(ctx, "GET", b.baseURL.String(), nil)
	if err != nil {
		logger.Errorf("Failed to create GET request: %v", err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	copyHeaders(req.Header, b.headers)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Errorf("SSE connect error: %v", err)
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var (
		eventName string
		dataLines []string
	)
	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimPrefix(strings.TrimSpace(line), "data:"))
			continue
		}
		if line == "" {
			payload := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]

			switch eventName {
			case "endpoint":
				if payload != "" {
					b.updatePostURL(payload)
					b.sendInitialize(ctx)
				}
			default:
				if isJSON(payload) {
					emitJSON(payload)
				} else {
					logger.Debugf("Skipping non-JSON SSE event (%q): %q", eventName, payload)
				}
			}
			eventName = ""
		}
	}

	if err := scanner.Err(); err != nil {
		logger.Errorf("SSE read error: %v", err)
	}
}

func isJSON(s string) bool {
	s = strings.TrimLeft(s, " \r\n\t")
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

func (b *StdioBridge) runLegacyWriter(ctx context.Context) {
	defer b.wg.Done()
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 10*1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		raw := scanner.Text()
		if strings.TrimSpace(raw) == "" {
			continue
		}
		var tmp json.RawMessage
		if err := json.Unmarshal([]byte(raw), &tmp); err != nil {
			logger.Errorf("Invalid JSON input: %v", err)
			continue
		}
		req, err := http.NewRequestWithContext(ctx, "POST", b.postURL.String(), strings.NewReader(raw))
		if err != nil {
			logger.Errorf("Failed to create HTTP request: %v", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		copyHeaders(req.Header, b.headers)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Errorf("POST error: %v", err)
			continue
		}
		err = resp.Body.Close()
		if err != nil {
			logger.Errorf("Failed to close response body: %v", err)
			continue
		}
	}
}

func (b *StdioBridge) updatePostURL(path string) {
	ep := strings.TrimSpace(path)
	if !strings.HasPrefix(ep, "http") {
		ep = b.baseURL.Scheme + "://" + b.baseURL.Host + ep
	}
	u, err := url.Parse(ep)
	if err != nil {
		logger.Errorf("Invalid SSE endpoint path: %q", path)
		return
	}
	b.postURL = u
}

func buildJSONRPCInitializeRequest() ([]byte, error) {
	req := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]interface{}{
			"protocolVersion": "2024-02-01",
			"capabilities": map[string]interface{}{
				"prompts": true,
				"tools":   true,
				"resources": map[string]interface{}{
					"subscribe":   true,
					"unsubscribe": true,
				},
			},
			"clientInfo": map[string]interface{}{
				"name":    "toolhive-stdio-bridge",
				"version": "0.1.0",
			},
		},
	}
	return json.Marshal(req)
}

func (b *StdioBridge) sendInitialize(ctx context.Context) {
	reqBody, err := buildJSONRPCInitializeRequest()
	if err != nil {
		logger.Errorf("Failed to marshal initialize request body: %v", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", b.postURL.String(), bytes.NewReader(reqBody))
	if err != nil {
		logger.Errorf("Failed to create HTTP request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	copyHeaders(req.Header, b.headers)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Errorf("POST initialize error: %v", err)
		return
	}
	err = resp.Body.Close()
	if err != nil {
		logger.Errorf("Failed to close response body: %v", err)
		return
	}
}

// Shared utilities:
func parseSSE(r io.Reader) {
	scanner := bufio.NewScanner(r)
	var sb strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			sb.WriteString(strings.TrimPrefix(line, "data:"))
		}
		if line == "" {
			raw := strings.TrimSpace(sb.String())
			sb.Reset()
			if raw != "" {
				emitJSON(raw)
			}
		}
	}
}

func emitJSON(raw string) {
	msg, err := jsonrpc2.DecodeMessage([]byte(raw))
	if err != nil {
		logger.Errorf("JSON-RPC decode error: %v", err)
		return
	}
	out, err := jsonrpc2.EncodeMessage(msg)
	if err != nil {
		logger.Errorf("JSON-RPC encode error: %v", err)
		return
	}
	fmt.Fprintln(os.Stdout, string(out))
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// Shutdown stops the transport and waits for all goroutines to finish.
func (b *StdioBridge) Shutdown() {
	if b.cancel != nil {
		b.cancel()
	}
	b.wg.Wait()
}
