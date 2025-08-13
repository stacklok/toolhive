package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// StdioBridge connects stdin/stdout to a target MCP server using the specified transport type.
type StdioBridge struct {
	mode      types.TransportType
	rawTarget string // upstream base URL

	up  *client.Client
	srv *server.MCPServer

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// NewStdioBridge creates a new StdioBridge instance for the given target URL and transport type.
func NewStdioBridge(rawURL string, mode types.TransportType) (*StdioBridge, error) {
	return &StdioBridge{mode: mode, rawTarget: rawURL}, nil
}

// Start initializes the bridge and connects to the upstream MCP server.
func (b *StdioBridge) Start(ctx context.Context) {
	ctx, b.cancel = context.WithCancel(ctx)
	b.wg.Add(1)
	go b.run(ctx)
}

// Shutdown gracefully stops the bridge, closing connections and waiting for cleanup.
func (b *StdioBridge) Shutdown() {
	if b.cancel != nil {
		b.cancel()
	}
	if b.up != nil {
		_ = b.up.Close()
	}
	b.wg.Wait()
}

func (b *StdioBridge) run(ctx context.Context) {
	logger.Infof("Starting StdioBridge for %s in mode %s", b.rawTarget, b.mode)
	defer b.wg.Done()

	up, err := b.connectUpstream(ctx)
	if err != nil {
		logger.Errorf("upstream connect failed: %v", err)
		return
	}
	b.up = up
	logger.Infof("Connected to upstream %s", b.rawTarget)

	if err := b.initializeUpstream(ctx); err != nil {
		logger.Errorf("upstream initialize failed: %v", err)
		return
	}
	logger.Infof("Upstream initialized successfully")

	// Tiny local stdio server
	b.srv = server.NewMCPServer(
		"toolhive-stdio-bridge",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, true),
		server.WithPromptCapabilities(true),
	)
	logger.Infof("Starting local stdio server")

	b.up.OnConnectionLost(func(err error) { logger.Warnf("upstream lost: %v", err) })

	// Handle upstream notifications
	b.up.OnNotification(func(n mcp.JSONRPCNotification) {
		logger.Infof("upstream → downstream notify: %s %v", n.Method, n.Params)
		// Convert the Params struct to JSON and back to a generic map
		var params map[string]any
		if buf, err := json.Marshal(n.Params); err != nil {
			logger.Warnf("Failed to marshal params: %v", err)
			params = map[string]any{}
		} else if err := json.Unmarshal(buf, &params); err != nil {
			logger.Warnf("Failed to unmarshal to map: %v", err)
			params = map[string]any{}
		}

		b.srv.SendNotificationToAllClients(n.Method, params)
	})

	// Forwarders (register once; no pagination/refresh to keep it simple)
	b.forwardAll(ctx)

	// Serve stdio (blocks)
	if err := server.ServeStdio(b.srv); err != nil {
		logger.Errorf("stdio server error: %v", err)
	}
}

func (b *StdioBridge) connectUpstream(_ context.Context) (*client.Client, error) {
	logger.Infof("Connecting to upstream %s using mode %s", b.rawTarget, b.mode)

	switch b.mode {
	case types.TransportTypeStreamableHTTP:
		c, err := client.NewStreamableHttpClient(
			b.rawTarget,
			transport.WithHTTPTimeout(0),
			transport.WithContinuousListening(),
		)
		if err != nil {
			return nil, err
		}
		// use separate, never-ending context for the client
		if err := c.Start(context.Background()); err != nil {
			return nil, err
		}
		return c, nil
	case types.TransportTypeSSE:
		c, err := client.NewSSEMCPClient(
			b.rawTarget,
		)
		if err != nil {
			return nil, err
		}
		if err := c.Start(context.Background()); err != nil {
			return nil, err
		}
		return c, nil
	case types.TransportTypeStdio:
		// if url contains sse it's sse else streamable-http
		var c *client.Client
		var err error
		if strings.Contains(b.rawTarget, "sse") {
			c, err = client.NewSSEMCPClient(
				b.rawTarget,
			)
			if err != nil {
				return nil, err
			}
		} else {
			c, err = client.NewStreamableHttpClient(
				b.rawTarget,
			)
			if err != nil {
				return nil, err
			}
		}
		if err := c.Start(context.Background()); err != nil {
			return nil, err
		}
		return c, nil
	case types.TransportTypeInspector:
		fallthrough
	default:
		return nil, fmt.Errorf("unsupported mode %q", b.mode)
	}
}

func (b *StdioBridge) initializeUpstream(ctx context.Context) error {
	logger.Infof("Initializing upstream %s", b.rawTarget)
	_, err := b.up.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcp.Implementation{Name: "toolhive-bridge", Version: "0.1.0"},
			Capabilities:    mcp.ClientCapabilities{},
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func (b *StdioBridge) forwardAll(ctx context.Context) {
	logger.Infof("Forwarding all upstream data to local stdio server")
	// Tools -> straight passthrough
	logger.Infof("Forwarding tools from upstream to local stdio server")
	if lt, err := b.up.ListTools(ctx, mcp.ListToolsRequest{}); err == nil {
		for _, tool := range lt.Tools {
			toolCopy := tool
			b.srv.AddTool(toolCopy, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return b.up.CallTool(ctx, req)
			})
		}
	}

	// Resources -> return []mcp.ResourceContents
	logger.Infof("Forwarding resources from upstream to local stdio server")
	if lr, err := b.up.ListResources(ctx, mcp.ListResourcesRequest{}); err == nil {
		for _, res := range lr.Resources {
			resCopy := res
			b.srv.AddResource(resCopy, func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				out, err := b.up.ReadResource(ctx, req)
				if err != nil {
					return nil, err
				}
				return out.Contents, nil
			})
		}
	}

	// Resource templates -> same return type as resources
	logger.Infof("Forwarding resource templates from upstream to local stdio server")
	if lt, err := b.up.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{}); err == nil {
		for _, tpl := range lt.ResourceTemplates {
			tplCopy := tpl
			b.srv.AddResourceTemplate(tplCopy, func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				out, err := b.up.ReadResource(ctx, req)
				if err != nil {
					return nil, err
				}
				return out.Contents, nil
			})
		}
	}

	// Prompts -> straight passthrough
	logger.Infof("Forwarding prompts from upstream to local stdio server")
	if lp, err := b.up.ListPrompts(ctx, mcp.ListPromptsRequest{}); err == nil {
		for _, p := range lp.Prompts {
			pCopy := p
			b.srv.AddPrompt(pCopy, func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				return b.up.GetPrompt(ctx, req)
			})
		}
	}
}
