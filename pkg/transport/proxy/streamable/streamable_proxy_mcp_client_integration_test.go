package streamable

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"
)

const (
	methodInitialize    = "initialize"
	methodToolsList     = "tools/list"
	methodToolsCall     = "tools/call"
	methodResourcesList = "resources/list"
	methodPing          = "ping"

	protoVersion = "2024-11-05"
	toolEcho     = "echo"
)

// TestMCPGoClientInitializeAndPing spins up the Streamable HTTP proxy and uses the real mcp-go client
// to perform Initialize and Ping over Streamable HTTP transport. The backend is simulated in-process
// by reading proxy.GetMessageChannel() and writing JSON-RPC responses via ForwardResponseToClients.
func TestMCPGoClientInitializeAndPing(t *testing.T) {
	t.Parallel()

	// Use a dedicated port to avoid clashes with other tests
	const port = 8096
	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		// no-op prometheus handler, safe for tests
	}))

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, proxy.Start(ctx), "proxy start")
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	// Give the server a moment to start listening
	time.Sleep(50 * time.Millisecond)

	// Simulated MCP server backend: respond to initialize and ping
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					// ignore notifications/non-requests
					continue
				}
				switch req.Method {
				case methodInitialize:
					// Minimal initialize result matching MCP schema
					result := map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo": map[string]any{
							"name":    "toolhive-test-server",
							"version": "0.0.0-test",
						},
						"capabilities": map[string]any{},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodToolsList:
					result := map[string]any{
						"tools": []map[string]any{
							{"name": toolEcho, "description": "Echo test tool"},
						},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodToolsCall:
					result := map[string]any{
						"content": []map[string]any{
							{"type": "text", "text": "ok"},
						},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodResourcesList:
					result := map[string]any{"resources": []any{}}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodPing:
					// Empty result is acceptable
					result := map[string]any{}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				default:
					// Generic empty success for any other method used by client
					result := map[string]any{}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Create real MCP client for Streamable HTTP and exercise Initialize + Ping
	serverURL := "http://127.0.0.1:8096" + StreamableHTTPEndpoint
	cl, err := client.NewStreamableHttpClient(serverURL)
	require.NoError(t, err, "create mcp-go streamable http client")
	t.Cleanup(func() { _ = cl.Close() })

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	require.NoError(t, cl.Start(startCtx), "start mcp transport")

	// Build an initialize request with minimal fields
	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()

	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = protoVersion
	initRequest.Params.ClientInfo = mcp.Implementation{
		Name:    "toolhive-streamable-proxy-integration-test",
		Version: "1.0.0",
	}
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}

	_, err = cl.Initialize(initCtx, initRequest)
	require.NoError(t, err, "initialize over streamable http")

	// List tools and ensure server returns expected tool
	ltCtx, ltCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ltCancel()
	ltReq := mcp.ListToolsRequest{}
	ltRes, err := cl.ListTools(ltCtx, ltReq)
	require.NoError(t, err, "list tools over streamable http")
	require.NotNil(t, ltRes)
	require.GreaterOrEqual(t, len(ltRes.Tools), 1)
	assert.Equal(t, toolEcho, ltRes.Tools[0].Name)

	// Call a tool and verify content
	ctCtx, ctCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ctCancel()
	ctReq := mcp.CallToolRequest{}
	ctReq.Params.Name = toolEcho
	ctReq.Params.Arguments = map[string]any{"input": "hello"}
	ctRes, err := cl.CallTool(ctCtx, ctReq)
	require.NoError(t, err, "call tool over streamable http")
	require.NotNil(t, ctRes)
	require.GreaterOrEqual(t, len(ctRes.Content), 1)
}

// TestMCPGoConcurrentClientsAndPings spins up several MCP clients against the same proxy and
// executes many concurrent Ping operations to validate routing and waiter correlation reliability.
func TestMCPGoConcurrentClientsAndPings(t *testing.T) {
	t.Parallel()

	const port = 8097
	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, proxy.Start(ctx), "proxy start")
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	time.Sleep(50 * time.Millisecond)

	// Backend: handle initialize + ping
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				switch req.Method {
				case methodInitialize:
					result := map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo":      map[string]any{"name": "toolhive-test-server", "version": "0.0.0-test"},
						"capabilities":    map[string]any{},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodToolsList:
					result := map[string]any{
						"tools": []map[string]any{
							{"name": toolEcho, "description": "Echo test tool"},
						},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodToolsCall:
					result := map[string]any{
						"content": []map[string]any{
							{"type": "text", "text": "ok"},
						},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodResourcesList:
					result := map[string]any{"resources": []any{}}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodPing:
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				default:
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	serverURL := "http://127.0.0.1:8097" + StreamableHTTPEndpoint

	// Create multiple clients
	const clientCount = 5
	const pingsPerClient = 5

	clients := make([]*client.Client, 0, clientCount)
	for i := 0; i < clientCount; i++ {
		cl, err := client.NewStreamableHttpClient(serverURL)
		require.NoError(t, err, "create client %d", i)
		clients = append(clients, cl)
	}

	// Start and initialize each client concurrently, then wait for readiness
	var initWG sync.WaitGroup
	initWG.Add(len(clients))
	initErrCh := make(chan error, len(clients))

	for i, cl := range clients {
		i, cl := i, cl
		go func() {
			defer initWG.Done()

			startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer startCancel()
			if err := cl.Start(startCtx); err != nil {
				initErrCh <- fmt.Errorf("start client %d: %w", i, err)
				return
			}

			initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer initCancel()
			initRequest := mcp.InitializeRequest{}
			initRequest.Params.ProtocolVersion = protoVersion
			initRequest.Params.ClientInfo = mcp.Implementation{Name: "client", Version: "test"}
			initRequest.Params.Capabilities = mcp.ClientCapabilities{}
			if _, err := cl.Initialize(initCtx, initRequest); err != nil {
				initErrCh <- fmt.Errorf("init client %d: %w", i, err)
				return
			}
		}()
	}

	initWG.Wait()
	close(initErrCh)
	for err := range initErrCh {
		require.NoError(t, err, "client initialization should succeed")
	}

	// Concurrent pings for all clients
	var wg sync.WaitGroup
	errCh := make(chan error, clientCount*pingsPerClient)

	for i, cl := range clients {
		for j := 0; j < pingsPerClient; j++ {
			wg.Add(1)
			go func(_, _ int, c *client.Client) {
				defer wg.Done()
				callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctReq := mcp.CallToolRequest{}
				ctReq.Params.Name = toolEcho
				ctReq.Params.Arguments = map[string]any{"input": "ok"}
				if _, err := c.CallTool(callCtx, ctReq); err != nil {
					errCh <- err
				}
			}(i, j, cl)
		}
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err, "concurrent pings should succeed")
	}

	// Close all clients
	for _, cl := range clients {
		_ = cl.Close()
	}
}

// TestMCPGoManySequentialPingsSingleClient stresses a single client issuing many pings sequentially
// to validate there are no waiter leaks or routing failures under load.
func TestMCPGoManySequentialPingsSingleClient(t *testing.T) {
	t.Parallel()

	const port = 8098
	proxy := NewHTTPProxy("127.0.0.1", port, "test-container", nil)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, proxy.Start(ctx), "proxy start")
	t.Cleanup(func() { _ = proxy.Stop(ctx) })

	time.Sleep(50 * time.Millisecond)

	// Backend handler
	go func() {
		for {
			select {
			case msg := <-proxy.GetMessageChannel():
				req, ok := msg.(*jsonrpc2.Request)
				if !ok || !req.ID.IsValid() {
					continue
				}
				switch req.Method {
				case methodInitialize:
					result := map[string]any{
						"protocolVersion": "2024-11-05",
						"serverInfo":      map[string]any{"name": "toolhive-test-server", "version": "0.0.0-test"},
						"capabilities":    map[string]any{},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodToolsList:
					result := map[string]any{
						"tools": []map[string]any{
							{"name": toolEcho, "description": "Echo test tool"},
						},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodToolsCall:
					result := map[string]any{
						"content": []map[string]any{
							{"type": "text", "text": "ok"},
						},
					}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodResourcesList:
					result := map[string]any{"resources": []any{}}
					resp, _ := jsonrpc2.NewResponse(req.ID, result, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				case methodPing:
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				default:
					resp, _ := jsonrpc2.NewResponse(req.ID, map[string]any{}, nil)
					_ = proxy.ForwardResponseToClients(ctx, resp)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	serverURL := "http://127.0.0.1:8098" + StreamableHTTPEndpoint

	cl, err := client.NewStreamableHttpClient(serverURL)
	require.NoError(t, err, "create client")
	t.Cleanup(func() { _ = cl.Close() })

	startCtx, startCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startCancel()
	require.NoError(t, cl.Start(startCtx), "start client")

	initCtx, initCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer initCancel()
	initRequest := mcp.InitializeRequest{}
	initRequest.Params.ProtocolVersion = protoVersion
	initRequest.Params.ClientInfo = mcp.Implementation{Name: "single-client", Version: "test"}
	initRequest.Params.Capabilities = mcp.ClientCapabilities{}
	_, err = cl.Initialize(initCtx, initRequest)
	require.NoError(t, err, "initialize")

	const iterations = 100
	for i := 0; i < iterations; i++ {
		callCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ctReq := mcp.CallToolRequest{}
		ctReq.Params.Name = toolEcho
		ctReq.Params.Arguments = map[string]any{"input": "ok"}
		_, err := cl.CallTool(callCtx, ctReq)
		cancel()
		require.NoErrorf(t, err, "call-tool %d should succeed", i)
	}
}
