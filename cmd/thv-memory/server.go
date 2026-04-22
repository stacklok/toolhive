// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"net/http"

	"github.com/mark3labs/mcp-go/server"
	"go.uber.org/zap"

	"github.com/stacklok/toolhive/cmd/thv-memory/tools"
	"github.com/stacklok/toolhive/pkg/memory"
)

const mcpEndpointPath = "/mcp"

// newHandler builds the MCP server, registers all memory tools, and returns an
// http.Handler that serves the MCP streamable-HTTP transport on /mcp plus a
// /health liveness probe.
func newHandler(cfg *Config, svc *memory.Service, store memory.Store, log *zap.Logger) http.Handler {
	s := server.NewMCPServer(cfg.Server.Name, cfg.Server.Version)

	tools.RegisterRemember(s, svc)
	tools.RegisterSearch(s, svc)
	tools.RegisterRecall(s, store)
	tools.RegisterForget(s, store)
	tools.RegisterUpdate(s, store)
	tools.RegisterFlag(s, store)
	tools.RegisterList(s, store)
	tools.RegisterConsolidate(s, svc, store)
	tools.RegisterCrystallize(s, store)

	log.Debug("registered memory MCP tools", zap.String("endpoint", mcpEndpointPath))

	streamable := server.NewStreamableHTTPServer(s,
		server.WithEndpointPath(mcpEndpointPath),
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", streamable)
	return mux
}
