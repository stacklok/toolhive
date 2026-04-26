// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/stacklok/toolhive/cmd/thv-memory/tools"
	"github.com/stacklok/toolhive/pkg/memory"
)

const (
	mcpEndpointPath     = "/mcp"
	resourceURIPrefix   = "memory://resource/"
	resourceURITemplate = "memory://resource/{id}"
)

// newMCPServer creates the MCP server and registers all memory tools.
// Resource capabilities (listChanged) are enabled so connected agents
// receive notifications/resources/list_changed whenever a resource is
// created or deleted via the management API.
func newMCPServer(cfg *Config, svc *memory.Service, store memory.Store) *server.MCPServer {
	s := server.NewMCPServer(cfg.Server.Name, cfg.Server.Version,
		server.WithResourceCapabilities(false, true),
	)

	tools.RegisterRemember(s, svc)
	tools.RegisterSearch(s, svc)
	tools.RegisterRecall(s, store)
	tools.RegisterForget(s, store)
	tools.RegisterUpdate(s, store)
	tools.RegisterFlag(s, store)
	tools.RegisterList(s, store)
	tools.RegisterConsolidate(s, svc, store)
	tools.RegisterCrystallize(s, store)

	// URI template allows agents to probe a resource by known ID without listing.
	s.AddResourceTemplate(
		mcp.NewResourceTemplate(resourceURITemplate, "Memory Resource",
			mcp.WithTemplateDescription("A UI-managed reference document stored in the memory server."),
			mcp.WithTemplateMIMEType("text/plain"),
		),
		server.ResourceTemplateHandlerFunc(makeResourceReadHandler(store)),
	)

	return s
}

// LoadExistingResources registers all persisted resource entries with the MCP
// server at startup so they appear in resources/list immediately.
func LoadExistingResources(ctx context.Context, s *server.MCPServer, store memory.Store, log *zap.Logger) {
	src := memory.SourceResource
	entries, err := store.List(ctx, memory.ListFilter{Source: &src, Limit: 1000})
	if err != nil {
		log.Warn("failed to load existing resources", zap.Error(err))
		return
	}
	for _, e := range entries {
		registerResource(s, store, e)
	}
	log.Debug("loaded existing resources", zap.Int("count", len(entries)))
}

// RegisterResourceEntry adds a resource entry to the MCP server listing.
// mcp-go automatically sends notifications/resources/list_changed to all
// connected sessions when WithResourceCapabilities listChanged is true.
func RegisterResourceEntry(s *server.MCPServer, store memory.Store, e memory.Entry) {
	// Remove any previous registration (e.g., on update with name change).
	s.DeleteResources(resourceURIPrefix + e.ID)
	registerResource(s, store, e)
}

// UnregisterResourceEntry removes a resource entry from the MCP server listing.
func UnregisterResourceEntry(s *server.MCPServer, id string) {
	s.DeleteResources(resourceURIPrefix + id)
}

func registerResource(s *server.MCPServer, store memory.Store, e memory.Entry) {
	name := resourceName(e)
	s.AddResource(
		mcp.NewResource(resourceURIPrefix+e.ID, name,
			mcp.WithResourceDescription(fmt.Sprintf("Resource entry %s", e.ID)),
			mcp.WithMIMEType("text/plain"),
		),
		makeResourceReadHandler(store),
	)
}

// newHandler wraps the MCP server in the streamable-HTTP transport and
// returns a mux that exposes:
//   - /mcp        — MCP streamable-HTTP transport
//   - /api/       — Management REST API (resource CRUD for UI)
//   - /health     — Liveness probe
func newHandler(s *server.MCPServer, resourceAPI http.Handler, log *zap.Logger) http.Handler {
	log.Debug("registered memory MCP tools", zap.String("endpoint", mcpEndpointPath))

	streamable := server.NewStreamableHTTPServer(s,
		server.WithEndpointPath(mcpEndpointPath),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/api/", resourceAPI)
	mux.Handle("/", streamable)
	return mux
}

// makeResourceReadHandler returns a handler that reads entry content from the
// store. When store is nil the handler is a no-op placeholder replaced at
// resource-registration time by AddResource with a proper store reference.
func makeResourceReadHandler(store memory.Store) server.ResourceHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		if store == nil {
			return nil, errors.New("resource store not initialised")
		}
		id, ok := strings.CutPrefix(req.Params.URI, resourceURIPrefix)
		if !ok || id == "" {
			return nil, fmt.Errorf("invalid resource URI: %s", req.Params.URI)
		}
		entry, err := store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      req.Params.URI,
				MIMEType: "text/plain",
				Text:     entry.Content,
			},
		}, nil
	}
}

// resourceName returns a short display name for a resource entry.
// Uses the first 60 characters of content as the name.
func resourceName(e memory.Entry) string {
	name := e.Content
	if len(name) > 60 {
		name = name[:60] + "…"
	}
	return name
}
