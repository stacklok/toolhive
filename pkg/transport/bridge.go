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
func NewStdioBridge(rawURL string) *StdioBridge {
	u, _ := url.Parse(rawURL)
	return &StdioBridge{baseURL: u, postURL: u}
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
			b.wg.Add(2 - 0)
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
	initReq := map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]interface{}{}}
	body, _ := json.Marshal(initReq)
	req, _ := http.NewRequestWithContext(ctx, "POST", b.baseURL.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	copyHeaders(req.Header, b.headers)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return "legacy-sse", nil
	}
	b.handleInitializeResponse(resp)
	return "streamable-http", nil
}

func (b *StdioBridge) handleInitializeResponse(resp *http.Response) {
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		if b.headers == nil {
			b.headers = make(http.Header)
		}
		b.headers.Set("Mcp-Session-Id", sid)
		logger.Infof("Streamable HTTP session ID: %s", sid)
	}
	data, _ := io.ReadAll(resp.Body)
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
	req, _ := http.NewRequestWithContext(ctx, "GET", b.baseURL.String(), nil)
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
		req, _ := http.NewRequestWithContext(ctx, "POST", b.baseURL.String(), strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		copyHeaders(req.Header, b.headers)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			logger.Errorf("POST error: %v", err)
			continue
		}
		ct := resp.Header.Get("Content-Type")
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
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
	req, _ := http.NewRequestWithContext(ctx, "GET", b.baseURL.String(), nil)
	req.Header.Set("Accept", "text/event-stream")
	copyHeaders(req.Header, b.headers)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger.Errorf("SSE connect error: %v", err)
		return
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	var sb strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			sb.WriteString(strings.TrimPrefix(line, "data:"))
		}
		if line == "" {
			raw := strings.TrimSpace(sb.String())
			sb.Reset()
			if strings.HasPrefix(raw, "/") && strings.Contains(raw, "sessionId") {
				b.updatePostURL(raw)
				b.sendInitialize(ctx)
			} else {
				emitJSON(raw)
			}
		}
	}
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
		req, _ := http.NewRequestWithContext(ctx, "POST", b.postURL.String(), strings.NewReader(raw))
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
	logger.Infof("POST URL updated to %s", b.postURL)
}

func (b *StdioBridge) sendInitialize(ctx context.Context) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]interface{}{},
	})
	req, _ := http.NewRequestWithContext(ctx, "POST", b.postURL.String(), bytes.NewReader(reqBody))
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
