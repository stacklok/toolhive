// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package mutating implements a mutating webhook middleware for ToolHive.
// It calls external HTTP services to transform MCP requests using JSONPatch (RFC 6902).
package mutating

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/webhook"
)

// MiddlewareParams holds the configuration parameters for the mutating webhook middleware.
type MiddlewareParams struct {
	// Webhooks is the list of mutating webhook configurations to call.
	// Webhooks are called in configuration order; each webhook receives the output
	// of the previous mutation. All patches are applied sequentially.
	Webhooks []webhook.Config `json:"webhooks"`
}

// Validate checks that the MiddlewareParams are valid.
func (p *MiddlewareParams) Validate() error {
	if len(p.Webhooks) == 0 {
		return fmt.Errorf("mutating webhook middleware requires at least one webhook")
	}
	for i, wh := range p.Webhooks {
		if err := wh.Validate(); err != nil {
			return fmt.Errorf("webhook[%d] (%q): %w", i, wh.Name, err)
		}
	}
	return nil
}

// FactoryMiddlewareParams extends MiddlewareParams with context for the factory.
type FactoryMiddlewareParams struct {
	MiddlewareParams
	// ServerName is the name of the ToolHive instance.
	ServerName string `json:"server_name"`
	// Transport is the transport type (e.g., sse, stdio).
	Transport string `json:"transport"`
}
