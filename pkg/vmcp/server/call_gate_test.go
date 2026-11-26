// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/core"
)

// gateFakeCore is a core.VMCP whose only exercised methods are the Check* triple.
// The embedded nil interface satisfies the rest and panics if the gate ever calls
// a method it should not.
type gateFakeCore struct {
	core.VMCP
	toolErr   error
	resErr    error
	promptErr error

	gotMethod     string
	gotResourceID string
	gotArgs       map[string]any
}

func (f *gateFakeCore) CheckToolCall(_ context.Context, _ *auth.Identity, name string, args map[string]any) error {
	f.gotMethod = "tools/call"
	f.gotResourceID = name
	f.gotArgs = args
	return f.toolErr
}

func (f *gateFakeCore) CheckResourceRead(_ context.Context, _ *auth.Identity, uri string) error {
	f.gotMethod = "resources/read"
	f.gotResourceID = uri
	return f.resErr
}

func (f *gateFakeCore) CheckPromptGet(_ context.Context, _ *auth.Identity, name string) error {
	f.gotMethod = "prompts/get"
	f.gotResourceID = name
	return f.promptErr
}

func TestAuthzCallGate(t *testing.T) {
	t.Parallel()

	deny := fmt.Errorf("%w: policy said no", vmcp.ErrAuthorizationFailed)
	infra := errors.New("aggregation exploded")

	tests := []struct {
		name string
		// parsed is the request the parsing middleware would have populated; nil means
		// no ParsedMCPRequest in context (unparsable body / batch).
		parsed       *mcpparser.ParsedMCPRequest
		core         *gateFakeCore
		wantDenial   bool
		wantMessage  string
		wantMethod   string // Check* method the gate must have invoked ("" = none)
		wantResource string
		wantArgs     map[string]any
		forbidInMsg  string // substring that must NOT appear in the denial message
	}{
		{
			name: "no parsed request admits",
			core: &gateFakeCore{},
		},
		{
			name:   "non-gated method admits",
			parsed: &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			core:   &gateFakeCore{},
		},
		{
			name: "tools/call allowed admits and forwards name+args",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call", ResourceID: "echo", Arguments: map[string]any{"input": "hi"},
			},
			core:         &gateFakeCore{},
			wantMethod:   "tools/call",
			wantResource: "echo",
			wantArgs:     map[string]any{"input": "hi"},
		},
		{
			name: "tools/call denied returns 403 denial",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call", ResourceID: "secret-tool", Arguments: map[string]any{"input": "hi"},
			},
			core:         &gateFakeCore{toolErr: deny},
			wantDenial:   true,
			wantMessage:  vmcp.DenyMessageToolCall,
			wantMethod:   "tools/call",
			wantResource: "secret-tool",
			forbidInMsg:  "secret-tool",
		},
		{
			name: "tools/call non-authz error admits (no 403 for infra faults)",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "tools/call", ResourceID: "echo",
			},
			core:       &gateFakeCore{toolErr: infra},
			wantMethod: "tools/call",
		},
		{
			name: "resources/read denied returns 403 denial",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "resources/read", ResourceID: "file://secret",
			},
			core:         &gateFakeCore{resErr: deny},
			wantDenial:   true,
			wantMessage:  vmcp.DenyMessageResourceRead,
			wantMethod:   "resources/read",
			wantResource: "file://secret",
			forbidInMsg:  "file://secret",
		},
		{
			name: "prompts/get denied returns 403 denial",
			parsed: &mcpparser.ParsedMCPRequest{
				Method: "prompts/get", ResourceID: "secret-prompt",
			},
			core:         &gateFakeCore{promptErr: deny},
			wantDenial:   true,
			wantMessage:  vmcp.DenyMessagePromptGet,
			wantMethod:   "prompts/get",
			wantResource: "secret-prompt",
			forbidInMsg:  "secret-prompt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Build the context per-subtest from t.Context() (our test convention),
			// injecting the parsed request as the parsing middleware would.
			ctx := t.Context()
			if tt.parsed != nil {
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, tt.parsed)
			}

			s := &Server{core: tt.core}
			gate := s.authzCallGate()

			denial := gate(ctx, nil)

			if tt.wantDenial {
				require.NotNil(t, denial, "expected a denial")
				assert.Equal(t, mcpparser.JSONRPCCodeDenied, denial.Code, "denial code must be 403")
				assert.Equal(t, 0, denial.HTTPStatus, "HTTPStatus must be zero so the shim writes 403")
				assert.Equal(t, tt.wantMessage, denial.Message)
				if tt.forbidInMsg != "" {
					assert.NotContains(t, denial.Message, tt.forbidInMsg,
						"the denial message must not leak the capability name (no enumeration oracle)")
				}
			} else {
				assert.Nil(t, denial, "expected the request to be admitted")
			}

			assert.Equal(t, tt.wantMethod, tt.core.gotMethod, "unexpected Check* method invoked")
			if tt.wantResource != "" {
				assert.Equal(t, tt.wantResource, tt.core.gotResourceID)
			}
			if tt.wantArgs != nil {
				assert.Equal(t, tt.wantArgs, tt.core.gotArgs)
			}
		})
	}
}
