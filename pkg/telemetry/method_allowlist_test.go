// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package telemetry

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

func TestSanitizeMCPMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		want   string
	}{
		{"empty is unknown", "", methodLabelUnknown},
		{"unknown stays unknown", methodLabelUnknown, methodLabelUnknown},

		// Known methods pass through verbatim.
		{"initialize", string(mcp.MethodInitialize), "initialize"},
		{"tools/call", string(mcp.MethodToolsCall), "tools/call"},
		{"tools/list", string(mcp.MethodToolsList), "tools/list"},
		{"prompts/get", string(mcp.MethodPromptsGet), "prompts/get"},
		{"resources/read", string(mcp.MethodResourcesRead), "resources/read"},
		{"ping", string(mcp.MethodPing), "ping"},
		{"notifications/initialized", "notifications/initialized", "notifications/initialized"},
		{"sampling/createMessage", "sampling/createMessage", "sampling/createMessage"},
		{"resources/subscribe", "resources/subscribe", "resources/subscribe"},

		// Scanner-supplied values from the report collapse to the sentinel.
		// None is a registered MCP method or ToolHive tool.
		{"eth debug_traceTransaction", "debug_traceTransaction", methodLabelOther},
		{"eth txpool_content", "txpool_content", methodLabelOther},
		{"cms User.filter", "User.filter", methodLabelOther},
		{"cms web.Login", "web.Login", methodLabelOther},

		// Near-miss and injection-shaped inputs collapse to the sentinel.
		{"case mismatch", "Tools/Call", methodLabelOther},
		{"trailing space", "tools/call ", methodLabelOther},
		{"arbitrary", "../../etc/passwd", methodLabelOther},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, sanitizeMCPMethod(tt.method))
		})
	}
}

// TestSanitizeMCPMethodBoundsCardinality confirms that a set of distinct
// attacker-controlled method values maps to a single label value.
func TestSanitizeMCPMethodBoundsCardinality(t *testing.T) {
	t.Parallel()

	attackerMethods := []string{
		"debug_traceTransaction",
		"txpool_content",
		"User.filter",
		"web.Login",
		"eth_getBalance",
		"admin_peers",
	}

	labels := make(map[string]struct{})
	for _, method := range attackerMethods {
		labels[sanitizeMCPMethod(method)] = struct{}{}
	}

	assert.Len(t, labels, 1, "distinct unknown methods must collapse to one label")
	_, ok := labels[methodLabelOther]
	assert.True(t, ok, "the single label must be the other sentinel")
}
