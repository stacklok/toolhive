package transport

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/versions"
)

// StdioBridge connects stdin/stdout to a target MCP server using the specified transport type.
type StdioBridge struct {
	name      string
	mode      types.TransportType
	rawTarget string // upstream base URL

	up  *mcp.ClientSession
	srv *mcp.Server

	wg     sync.WaitGroup
	cancel context.CancelFunc
}

// NewStdioBridge creates a new StdioBridge instance for the given target URL and transport type.
func NewStdioBridge(name, rawURL string, mode types.TransportType) (*StdioBridge, error) {
	return &StdioBridge{
		name:      name,
		mode:      mode,
		rawTarget: rawURL,
	}, nil
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
	b.srv = mcp.NewServer(
		&mcp.Implementation{
			Name:    fmt.Sprintf("thv-%s", b.name),
			Version: versions.Version,
		},
		&mcp.ServerOptions{
			HasTools:     true,
			HasResources: true,
			HasPrompts:   true,
		},
	)
	logger.Infof("Starting local stdio server")

	// TODO: The new SDK doesn't have OnConnectionLost and OnNotification methods on ClientSession.
	// Need to investigate the new pattern for handling disconnections and notifications.
	// b.up.OnConnectionLost(func(err error) { logger.Warnf("upstream lost: %v", err) })

	// Handle upstream notifications
	// TODO: The new SDK doesn't support SendNotificationToAllClients.
	// Need to investigate how to forward notifications between client and server sessions.
	// b.up.OnNotification(func(n mcp.JSONRPCNotification) {
	// 	logger.Infof("upstream → downstream notify: %s %v", n.Method, n.Params)
	// 	// Convert the Params struct to JSON and back to a generic map
	// 	var params map[string]any
	// 	if buf, err := json.Marshal(n.Params); err != nil {
	// 		logger.Warnf("Failed to marshal params: %v", err)
	// 		params = map[string]any{}
	// 	} else if err := json.Unmarshal(buf, &params); err != nil {
	// 		logger.Warnf("Failed to unmarshal to map: %v", err)
	// 		params = map[string]any{}
	// 	}

	// 	b.srv.SendNotificationToAllClients(n.Method, params)
	// })

	// Forwarders (register once; no pagination/refresh to keep it simple)
	b.forwardAll(ctx)

	// Serve stdio (blocks)
	if err := b.srv.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		logger.Errorf("stdio server error: %v", err)
	}
}

func (b *StdioBridge) connectUpstream(ctx context.Context) (*mcp.ClientSession, error) {
	logger.Infof("Connecting to upstream %s using mode %s", b.rawTarget, b.mode)

	// Create the MCP client
	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "toolhive-bridge",
			Version: versions.Version,
		},
		&mcp.ClientOptions{},
	)

	var transport mcp.Transport

	switch b.mode {
	case types.TransportTypeStreamableHTTP:
		transport = &mcp.StreamableClientTransport{
			Endpoint:   b.rawTarget,
			MaxRetries: 5,
		}
	case types.TransportTypeSSE:
		transport = &mcp.SSEClientTransport{
			Endpoint: b.rawTarget,
		}
	case types.TransportTypeStdio:
		// if url contains sse it's sse else streamable-http
		if strings.Contains(b.rawTarget, "sse") {
			transport = &mcp.SSEClientTransport{
				Endpoint: b.rawTarget,
			}
		} else {
			transport = &mcp.StreamableClientTransport{
				Endpoint:   b.rawTarget,
				MaxRetries: 5,
			}
		}
	case types.TransportTypeInspector:
		fallthrough
	default:
		return nil, fmt.Errorf("unsupported mode %q", b.mode)
	}

	// Connect using the transport
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		return nil, err
	}

	return session, nil
}

func (b *StdioBridge) initializeUpstream(ctx context.Context) error {
	logger.Infof("Initializing upstream %s", b.rawTarget)
	// Initialize is handled during Connect, just verify we're connected
	if b.up == nil {
		return fmt.Errorf("upstream not connected")
	}
	result := b.up.InitializeResult()
	if result == nil {
		return fmt.Errorf("upstream not initialized")
	}
	logger.Infof("Upstream initialized with protocol version: %s", result.ProtocolVersion)
	return nil
}

func (b *StdioBridge) forwardAll(ctx context.Context) {
	logger.Infof("Forwarding all upstream data to local stdio server")
	// Tools -> straight passthrough
	logger.Infof("Forwarding tools from upstream to local stdio server")
	if lt, err := b.up.ListTools(ctx, &mcp.ListToolsParams{}); err == nil {
		for _, tool := range lt.Tools {
			toolCopy := tool
			b.srv.AddTool(toolCopy, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return b.up.CallTool(ctx, &mcp.CallToolParams{
					Name:      req.Params.Name,
					Arguments: req.Params.Arguments,
				})
			})
		}
	}

	// Resources -> return []mcp.ResourceContents
	logger.Infof("Forwarding resources from upstream to local stdio server")
	if lr, err := b.up.ListResources(ctx, &mcp.ListResourcesParams{}); err == nil {
		for _, res := range lr.Resources {
			resCopy := res
			b.srv.AddResource(resCopy, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return b.up.ReadResource(ctx, &mcp.ReadResourceParams{
					URI: req.Params.URI,
				})
			})
		}
	}

	// Resource templates -> same return type as resources
	logger.Infof("Forwarding resource templates from upstream to local stdio server")
	if lt, err := b.up.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{}); err == nil {
		for _, tpl := range lt.ResourceTemplates {
			tplCopy := tpl
			b.srv.AddResourceTemplate(tplCopy, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
				return b.up.ReadResource(ctx, &mcp.ReadResourceParams{
					URI: req.Params.URI,
				})
			})
		}
	}

	// Prompts -> straight passthrough
	logger.Infof("Forwarding prompts from upstream to local stdio server")
	if lp, err := b.up.ListPrompts(ctx, &mcp.ListPromptsParams{}); err == nil {
		for _, p := range lp.Prompts {
			pCopy := p
			b.srv.AddPrompt(pCopy, func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				return b.up.GetPrompt(ctx, &mcp.GetPromptParams{
					Name:      req.Params.Name,
					Arguments: req.Params.Arguments,
				})
			})
		}
	}
}
