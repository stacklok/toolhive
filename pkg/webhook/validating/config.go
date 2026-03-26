// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package validating implements a validating webhook middleware for ToolHive.
// It calls external HTTP services to approve or deny MCP requests.
package validating

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/webhook"
)

// MiddlewareParams holds the configuration parameters for the validating webhook middleware.
type MiddlewareParams struct {
	// Webhooks is the list of validating webhook configurations to call.
	// Webhooks are called in configuration order; if any webhook denies the request,
	// the request is rejected. All webhooks must allow the request for it to proceed.
	Webhooks []webhook.Config `json:"webhooks"`
}

// Validate checks that the MiddlewareParams are valid.
func (p *MiddlewareParams) Validate() error {
	if len(p.Webhooks) == 0 {
		return fmt.Errorf("validating webhook middleware requires at least one webhook")
	}
	for i, wh := range p.Webhooks {
		if err := wh.Validate(); err != nil {
			return fmt.Errorf("webhook[%d] (%q): %w", i, wh.Name, err)
		}
	}
	return nil
}
