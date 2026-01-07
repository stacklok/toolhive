package testkit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const (
	toolsListRequest = `{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {}}`
)

// streamableServer provides a test server with /mcp-json and /mcp-sse endpoints
type streamableServer struct {
	middlewares       []func(http.Handler) http.Handler
	toolsListResponse string
	tools             map[string]tooldef
	clientType        clientType
	withProxy         bool
	connHangDuration  time.Duration
}

var _ TestMCPServer = (*streamableServer)(nil)

func (s *streamableServer) SetMiddlewares(middlewares ...func(http.Handler) http.Handler) error {
	if len(s.middlewares) > 0 {
		return fmt.Errorf("middlewares already set")
	}
	s.middlewares = middlewares
	return nil
}

func (s *streamableServer) AddTool(tool tooldef) error {
	if _, ok := s.tools[tool.Name]; ok {
		return fmt.Errorf("tool %s already exists", tool.Name)
	}
	if s.tools == nil {
		s.tools = make(map[string]tooldef)
	}
	s.tools[tool.Name] = tool
	return nil
}

func (s *streamableServer) SetClientType(clientType clientType) error {
	if s.clientType != "" {
		return fmt.Errorf("client type already set")
	}
	s.clientType = clientType
	return nil
}

func (s *streamableServer) SetWithProxy() error {
	s.withProxy = true
	return nil
}

func (s *streamableServer) SetConnectionHang(duration time.Duration) error {
	s.connHangDuration = duration
	return nil
}

type streamableJSONClient struct {
	server *httptest.Server
}

var _ TestMCPClient = (*streamableJSONClient)(nil)

func (s *streamableJSONClient) ToolsList() ([]byte, error) {
	client := s.server.Client()
	resp, err := client.Post(s.server.URL+"/mcp-json", "application/json", bytes.NewBufferString(toolsListRequest))
	if err != nil {
		return nil, err
	}
	defer func() {
		// Error ignored in test cleanup
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

func (s *streamableJSONClient) ToolsCall(name string) ([]byte, error) {
	client := s.server.Client()

	toolsCallRequest := fmt.Sprintf(`{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "%s"}}`, name)
	resp, err := client.Post(s.server.URL+"/mcp-json", "application/json", bytes.NewBufferString(toolsCallRequest))
	if err != nil {
		return nil, err
	}
	defer func() {
		// Error ignored in test cleanup
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return body, nil
}

type streamableEventStreamClient struct {
	server *httptest.Server
}

var _ TestMCPClient = (*streamableEventStreamClient)(nil)

func (s *streamableEventStreamClient) ToolsList() ([]byte, error) {
	client := s.server.Client()
	resp, err := client.Post(s.server.URL+"/mcp-sse", "application/json", bytes.NewBufferString(toolsListRequest))
	if err != nil {
		return nil, err
	}
	defer func() {
		// Error ignored in test cleanup
		_ = resp.Body.Close()
	}()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(NewSplitSSE(LFSep))

	for scanner.Scan() {
		if scanner.Err() != nil {
			return nil, scanner.Err()
		}

		lineScanner := bufio.NewScanner(bytes.NewReader(scanner.Bytes()))
		for lineScanner.Scan() {
			if lineScanner.Err() != nil {
				return nil, lineScanner.Err()
			}

			if data, ok := bytes.CutPrefix(lineScanner.Bytes(), []byte("data:")); ok {
				return data, nil
			}
		}
	}

	return nil, errors.New("no data found")
}

func (s *streamableEventStreamClient) ToolsCall(name string) ([]byte, error) {
	client := s.server.Client()

	toolsCallRequest := fmt.Sprintf(`{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "%s"}}`, name)
	resp, err := client.Post(s.server.URL+"/mcp-sse", "application/json", bytes.NewBufferString(toolsCallRequest))
	if err != nil {
		return nil, err
	}
	defer func() {
		// Error ignored in test cleanup
		_ = resp.Body.Close()
	}()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Split(NewSplitSSE(LFSep))

	for scanner.Scan() {
		if scanner.Err() != nil {
			return nil, scanner.Err()
		}

		lineScanner := bufio.NewScanner(bytes.NewReader(scanner.Bytes()))
		for lineScanner.Scan() {
			if lineScanner.Err() != nil {
				return nil, lineScanner.Err()
			}

			if data, ok := bytes.CutPrefix(lineScanner.Bytes(), []byte("data:")); ok {
				var result map[string]any
				err := json.Unmarshal([]byte(data), &result)
				if err != nil {
					return nil, err
				}
				return []byte(data), nil
			}
		}
	}

	return nil, errors.New("no data found")
}

// NewStreamableTestServer creates a new Streamable-HTTP server,
// wraps it in an `httptest.Server`, and returns it.
func NewStreamableTestServer(
	options ...TestMCPServerOption,
) (*httptest.Server, TestMCPClient, error) {
	var testServer *httptest.Server
	server := &streamableServer{}

	for _, option := range options {
		if err := option(server); err != nil {
			return nil, nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	// This precompiles the tools list response based on the provided tools
	server.toolsListResponse = makeToolsList(server.tools)

	allMiddlewares := append(
		[]func(http.Handler) http.Handler{
			middleware.RequestID,
			middleware.Recoverer,
		},
		server.middlewares...,
	)

	router := chi.NewRouter()

	// If the server is not configured to use a proxy, apply the middlewares to
	// the router directly.
	if !server.withProxy {
		router.Use(allMiddlewares...)
	}

	router.Post("/mcp-json", server.mcpJSONHandler)
	router.Post("/mcp-sse", server.mcpEventStreamHandler)

	// Start backend test server
	backendServer := httptest.NewServer(router)

	// By default, use the backend test server directly.
	testServer = backendServer

	// If the server is configured to use a proxy,create a reverse proxy to
	// the backend test server.
	if server.withProxy {
		proxyServer, err := wrapBackendWithProxy(backendServer.URL, allMiddlewares)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to wrap backend with proxy: %w", err)
		}
		testServer = proxyServer
	}

	switch server.clientType {
	case clientTypeJSON:
		return testServer, &streamableJSONClient{
			server: testServer,
		}, nil
	case clientTypeSSE:
		return testServer, &streamableEventStreamClient{
			server: testServer,
		}, nil
	default:
		return testServer, &streamableJSONClient{
			server: testServer,
		}, nil
	}
}

func (s *streamableServer) mcpJSONHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Error reading request body: %v", err), http.StatusBadRequest)
		return
	}

	// Parse the MCP request to validate it's either tools/list or tools/call
	var mcpRequest map[string]any
	if err := json.Unmarshal(body, &mcpRequest); err != nil {
		http.Error(w, fmt.Sprintf("Invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	// Check if it's a valid MCP request with method
	method, ok := mcpRequest["method"].(string)
	if !ok {
		http.Error(w, "Missing or invalid method", http.StatusBadRequest)
		return
	}

	// Validate that it's either tools/list or tools/call
	if method != "tools/list" && method != "tools/call" {
		http.Error(w, "Unsupported method: "+method, http.StatusBadRequest)
		return
	}

	// Generate appropriate response based on method
	var response string
	switch method {
	case toolsListMethod:
		response = s.toolsListResponse
	case toolsCallMethod:
		response = runToolCall(s.tools, mcpRequest)
	default:
		//nolint:goconst
		response = "failed to generate response"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if s.connHangDuration == 0 {
		singleFlushResponse([]byte(response), w)
	} else {
		staggeredFlushResponse([]byte(response), w, s.connHangDuration)
	}
}

func (s *streamableServer) mcpEventStreamHandler(
	w http.ResponseWriter,
	r *http.Request,
) {
	// Read the request body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Error reading request body", http.StatusInternalServerError)
		return
	}

	// Parse the MCP request to validate it's either tools/list or tools/call
	var mcpRequest map[string]any
	if err := json.Unmarshal(body, &mcpRequest); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Check if it's a valid MCP request with method
	method, ok := mcpRequest["method"].(string)
	if !ok {
		http.Error(w, "Missing or invalid method", http.StatusBadRequest)
		return
	}

	// Validate that it's either tools/list or tools/call
	if method != "tools/list" && method != "tools/call" {
		http.Error(w, "Unsupported method: "+method, http.StatusBadRequest)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")

	// Generate appropriate SSE response based on method
	var response string
	switch method {
	case toolsListMethod:
		response = s.toolsListResponse
	case toolsCallMethod:
		response = runToolCall(s.tools, mcpRequest)
	default:
		//nolint:goconst
		response = "failed to generate response"
	}

	response = "event: random-stuff\ndata: " + response + "\n\n"

	if s.connHangDuration == 0 {
		singleFlushResponse([]byte(response), w)
	} else {
		staggeredFlushResponse([]byte(response), w, s.connHangDuration)
	}
}
