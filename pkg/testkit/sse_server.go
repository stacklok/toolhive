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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// sseServer provides a test server with /command and /sse endpoints
type sseServer struct {
	commandChannel chan string

	middlewares       []func(http.Handler) http.Handler
	toolsListResponse string
	tools             map[string]tooldef
	clientType        clientType
}

var _ TestMCPServer = (*sseServer)(nil)

func (s *sseServer) SetMiddlewares(middlewares ...func(http.Handler) http.Handler) error {
	if len(s.middlewares) > 0 {
		return fmt.Errorf("middlewares already set")
	}
	s.middlewares = middlewares
	return nil
}

func (s *sseServer) AddTool(tool tooldef) error {
	if _, ok := s.tools[tool.Name]; ok {
		return fmt.Errorf("tool %s already exists", tool.Name)
	}
	if s.tools == nil {
		s.tools = make(map[string]tooldef)
	}
	s.tools[tool.Name] = tool
	return nil
}

func (s *sseServer) SetClientType(clientType clientType) error {
	if s.clientType != "" {
		return fmt.Errorf("client type already set")
	}
	s.clientType = clientType
	return nil
}

type sseEventStreamClient struct {
	server         *httptest.Server
	commandChannel chan []byte
}

var _ TestMCPClient = (*sseEventStreamClient)(nil)

func (s *sseEventStreamClient) ToolsList() ([]byte, error) {
	client := s.server.Client()

	resp, err := client.Post(s.server.URL+"/command", "application/json", bytes.NewBufferString(toolsListRequest))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	body := <-s.commandChannel
	scanner := bufio.NewScanner(bytes.NewReader(body))
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
				return data, nil
			}
		}
	}

	return nil, errors.New("no data found")
}

func (s *sseEventStreamClient) ToolsCall(name string) ([]byte, error) {
	client := s.server.Client()

	toolsCallRequest := fmt.Sprintf(`{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {"name": "%s"}}`, name)
	resp, err := client.Post(s.server.URL+"/command", "application/json", bytes.NewBufferString(toolsCallRequest))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	_, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	body := <-s.commandChannel
	scanner := bufio.NewScanner(bytes.NewReader(body))
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

// NewSSETestServer creates a new SSE server, wraps it
// in an `httptest.Server`, and returns it.
func NewSSETestServer(
	options ...TestMCPServerOption,
) (*httptest.Server, TestMCPClient, error) {
	commandChannel := make(chan string, 10)

	server := &sseServer{
		commandChannel: commandChannel,
	}

	for _, option := range options {
		if err := option(server); err != nil {
			return nil, nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	if server.tools != nil {
		// This precompiles the tools list response based on the provided tools
		server.toolsListResponse = makeToolsList(server.tools)
	}

	router := chi.NewRouter()

	// Apply middleware
	allMiddlewares := append(
		[]func(http.Handler) http.Handler{
			middleware.RequestID,
			middleware.Recoverer,
		},
		server.middlewares...,
	)
	router.Use(allMiddlewares...)

	router.Post("/command", server.commandHandler)
	router.Get("/sse", server.sseHandler)

	httpServer := httptest.NewServer(router)

	clientCommandChannel := make(chan []byte, 1)
	go func() {
		defer close(clientCommandChannel)

		resp, err := httpServer.Client().Get(httpServer.URL + "/sse")
		if err != nil {
			return
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return
		}

		clientCommandChannel <- body
	}()

	switch server.clientType {
	case clientTypeJSON:
		return nil, nil, fmt.Errorf("client type JSON not supported for SSE server")
	case clientTypeSSE:
		return httpServer, &sseEventStreamClient{
			server:         httpServer,
			commandChannel: clientCommandChannel,
		}, nil
	default:
		return httpServer, &sseEventStreamClient{
			server:         httpServer,
			commandChannel: clientCommandChannel,
		}, nil
	}
}

func (s *sseServer) commandHandler(w http.ResponseWriter, r *http.Request) {
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
	if method != toolsListMethod && method != toolsCallMethod {
		http.Error(w, "Unsupported method: "+method, http.StatusBadRequest)
		return
	}

	// Send the command to the channel for /sse endpoint
	s.commandChannel <- string(body)

	// Reply with "Accepted"
	w.WriteHeader(http.StatusAccepted)
	if _, err := w.Write([]byte("Accepted")); err != nil {
		http.Error(w, "Error writing response", http.StatusInternalServerError)
		return
	}

	// Flush if available
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}

	// Note: it is paramount to close the channel as it starts a chain reaction
	// that causes the whole connection to be closed, allowing the test to finish.
	close(s.commandChannel)
}

func (s *sseServer) sseHandler(w http.ResponseWriter, _ *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")

	// Get flusher for streaming responses
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Loop over commands from the channel
	for command := range s.commandChannel {
		// Parse the MCP request to determine the response
		var mcpRequest map[string]any
		if err := json.Unmarshal([]byte(command), &mcpRequest); err != nil {
			// If parsing fails, send the raw command as before
			if _, err := w.Write([]byte("data: " + command + "\n\n")); err != nil {
				http.Error(w, "Error writing response", http.StatusInternalServerError)
				return
			}
		} else {
			// Generate appropriate response based on method
			method, ok := mcpRequest["method"].(string)
			if !ok {
				// If no method, send the raw command as before
				if _, err := w.Write([]byte("data: " + command + "\n\n")); err != nil {
					http.Error(w, "Error writing response", http.StatusInternalServerError)
					return
				}
			} else {
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

				if _, err := w.Write([]byte("event: random-stuff\ndata: " + response + "\n\n")); err != nil {
					http.Error(w, "Error writing response", http.StatusInternalServerError)
					return
				}
			}

			// Flush the response immediately
			flusher.Flush()
		}
	}
}
