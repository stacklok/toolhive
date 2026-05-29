// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package toxicflow

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpclient "github.com/stacklok/toolhive/pkg/mcp/client"
)

// probeTimeout bounds a single live tools/list probe so a slow or wedged
// server cannot hang the audit.
const probeTimeout = 10 * time.Second

// probeAnnotations connects to a running server through the ToolHive proxy and
// reads its live tool annotations (notably openWorldHint), which are the
// strongest available signal for the source role. It is best-effort: callers
// treat any error as "no live data" rather than failing the audit.
func probeAnnotations(ctx context.Context, serverURL, transport string) (map[string]*authorizers.ToolAnnotations, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	if transport == "" {
		transport = mcpclient.TransportAuto
	}
	c, err := mcpclient.Connect(ctx, serverURL, transport, "toolhive-trifecta-audit")
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", serverURL, err)
	}
	defer func() { _ = c.Close() }()

	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	return mapAnnotations(res.Tools), nil
}

// mapAnnotations converts MCP tool annotations into the ToolHive representation
// classifySource consumes. Kept separate so the field-by-field copy is unit
// testable without a live server.
func mapAnnotations(tools []mcp.Tool) map[string]*authorizers.ToolAnnotations {
	annotations := make(map[string]*authorizers.ToolAnnotations, len(tools))
	for i := range tools {
		a := tools[i].Annotations
		annotations[tools[i].Name] = &authorizers.ToolAnnotations{
			ReadOnlyHint:    a.ReadOnlyHint,
			DestructiveHint: a.DestructiveHint,
			IdempotentHint:  a.IdempotentHint,
			OpenWorldHint:   a.OpenWorldHint,
		}
	}
	return annotations
}
