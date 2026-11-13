// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// recordingPDP is a fake PDP that records each operation it is asked and answers
// allow for the operations in allowed.
type recordingPDP struct {
	mu      sync.Mutex
	asked   []string
	allowed map[string]bool
}

func (p *recordingPDP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var porc map[string]any
	if err := json.NewDecoder(r.Body).Decode(&porc); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	op, _ := porc["operation"].(string)
	p.mu.Lock()
	p.asked = append(p.asked, op)
	allow := p.allowed[op]
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"allow": allow})
}

// visibilityThroughPDP runs the tools/list visibility decision for "weather" through
// the same decorator the filters use, and returns the answer and what the PDP was asked.
func visibilityThroughPDP(t *testing.T, forward bool, allowed map[string]bool) (bool, []string) {
	t.Helper()
	pdp := &recordingPDP{allowed: allowed}
	server := httptest.NewServer(pdp)
	defer server.Close()

	authorizer, err := NewAuthorizer(ConfigOptions{
		HTTP:            &ConnectionConfig{URL: server.URL},
		ClaimMapping:    "standard",
		ForwardToolRead: forward,
	}, "test-server")
	if err != nil {
		t.Fatalf("failed to create authorizer: %v", err)
	}
	defer func() { _ = authorizer.Close() }()

	ctx := auth.WithIdentity(context.Background(), &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"sub": "alice", "roles": []any{"reader"}}},
	})
	ok, err := authorizers.WithToolCallImpliesRead(authorizer).AuthorizeWithJWTClaims(
		ctx, authorizers.MCPFeatureTool, authorizers.MCPOperationRead, "weather", nil)
	if err != nil {
		t.Fatalf("visibility check failed: %v", err)
	}
	pdp.mu.Lock()
	defer pdp.mu.Unlock()
	return ok, append([]string(nil), pdp.asked...)
}

func TestForwardToolRead_DefaultAsksOnlyToolCallOnce(t *testing.T) {
	t.Parallel()

	ok, asked := visibilityThroughPDP(t, false, map[string]bool{"mcp:tool:call": true})

	if !ok {
		t.Fatal("a tool:call allow must make the tool visible by default")
	}
	if len(asked) != 1 || asked[0] != "mcp:tool:call" {
		t.Fatalf("default mode must ask the PDP exactly once for mcp:tool:call, got %v", asked)
	}
}

func TestForwardToolRead_DefaultCallDenyHidesTool(t *testing.T) {
	t.Parallel()

	// Reads are allowed, calls are forbidden: by default the call decides, so the tool is hidden.
	ok, asked := visibilityThroughPDP(t, false, map[string]bool{"mcp:tool:read": true})

	if ok {
		t.Fatal("by default a call-only forbid must keep hiding the tool, even if read is allowed")
	}
	if len(asked) != 1 || asked[0] != "mcp:tool:call" {
		t.Fatalf("default mode must not send mcp:tool:read to the PDP, got %v", asked)
	}
}

func TestForwardToolRead_FlagAsksToolReadOnceNoFallback(t *testing.T) {
	t.Parallel()

	ok, asked := visibilityThroughPDP(t, true, map[string]bool{"mcp:tool:read": true})

	if !ok {
		t.Fatal("with forward_tool_read a tool:read allow must make the tool visible")
	}
	if len(asked) != 1 || asked[0] != "mcp:tool:read" {
		t.Fatalf("forward_tool_read must ask the PDP exactly once for mcp:tool:read, got %v", asked)
	}
}

func TestForwardToolRead_FlagReadDenyDoesNotFallBackToCall(t *testing.T) {
	t.Parallel()

	// The PDP denies read but would allow call. With the flag set the PDP owns the rule,
	// so the tool stays hidden and the PDP is never asked for tool:call.
	ok, asked := visibilityThroughPDP(t, true, map[string]bool{"mcp:tool:call": true})

	if ok {
		t.Fatal("with forward_tool_read a read deny must hide the tool, with no call fallback")
	}
	if len(asked) != 1 || asked[0] != "mcp:tool:read" {
		t.Fatalf("forward_tool_read must ask only mcp:tool:read, got %v", asked)
	}
}

func TestForwardToolRead_ConfigDefaultsFalseAndParses(t *testing.T) {
	t.Parallel()

	var defaulted ConfigOptions
	if err := json.Unmarshal([]byte(`{"http":{"url":"http://pdp"},"claim_mapping":"standard"}`), &defaulted); err != nil {
		t.Fatalf("unmarshal default config: %v", err)
	}
	if defaulted.ForwardToolRead {
		t.Fatal("forward_tool_read must default to false")
	}

	var enabled ConfigOptions
	if err := json.Unmarshal([]byte(`{"http":{"url":"http://pdp"},"claim_mapping":"standard","forward_tool_read":true}`), &enabled); err != nil {
		t.Fatalf("unmarshal enabled config: %v", err)
	}
	if !enabled.ForwardToolRead {
		t.Fatal("forward_tool_read: true must parse to true")
	}
}
