// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive-core/mcpcompat/server"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// fakeSDKElicitationRequester captures the translated mcp-go request and returns
// a canned response/error, letting us assert the adapter's domain ⇄ mcp-go mapping.
type fakeSDKElicitationRequester struct {
	gotRequest mcp.ElicitationRequest
	result     *mcp.ElicitationResult
	err        error
}

func (f *fakeSDKElicitationRequester) RequestElicitation(
	_ context.Context,
	request mcp.ElicitationRequest,
) (*mcp.ElicitationResult, error) {
	f.gotRequest = request
	return f.result, f.err
}

func TestSDKElicitationAdapter_RequestElicitation(t *testing.T) {
	t.Parallel()

	content := map[string]any{"confirmed": true}

	tests := []struct {
		name        string
		req         vmcp.ElicitationRequest
		result      *mcp.ElicitationResult
		sdkErr      error
		wantError   bool
		wantAction  string
		wantContent any
	}{
		{
			name: "accept round-trips action and content",
			req: vmcp.ElicitationRequest{
				Message:         "Confirm?",
				RequestedSchema: map[string]any{"type": "object"},
			},
			result: &mcp.ElicitationResult{
				ElicitationResponse: mcp.ElicitationResponse{
					Action:  mcp.ElicitationResponseActionAccept,
					Content: content,
				},
			},
			wantAction:  "accept",
			wantContent: content,
		},
		{
			name: "decline maps action with nil content",
			req:  vmcp.ElicitationRequest{Message: "Proceed?"},
			result: &mcp.ElicitationResult{
				ElicitationResponse: mcp.ElicitationResponse{
					Action: mcp.ElicitationResponseActionDecline,
				},
			},
			wantAction:  "decline",
			wantContent: nil,
		},
		{
			name: "cancel maps action with nil content",
			req:  vmcp.ElicitationRequest{Message: "Continue?"},
			result: &mcp.ElicitationResult{
				ElicitationResponse: mcp.ElicitationResponse{
					Action: mcp.ElicitationResponseActionCancel,
				},
			},
			wantAction:  "cancel",
			wantContent: nil,
		},
		{
			name:      "SDK error is propagated",
			req:       vmcp.ElicitationRequest{Message: "Confirm?"},
			sdkErr:    errors.New("SDK internal error"),
			wantError: true,
		},
		{
			name:      "context cancellation is propagated",
			req:       vmcp.ElicitationRequest{Message: "Confirm?"},
			sdkErr:    context.Canceled,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fake := &fakeSDKElicitationRequester{result: tt.result, err: tt.sdkErr}
			adapter := &sdkElicitationAdapter{mcpServer: fake}

			result, err := adapter.RequestElicitation(context.Background(), tt.req)

			// Request translation: domain fields map onto mcp.ElicitationParams.
			assert.Equal(t, tt.req.Message, fake.gotRequest.Params.Message)
			assert.Equal(t, tt.req.RequestedSchema, fake.gotRequest.Params.RequestedSchema)

			if tt.wantError {
				require.Error(t, err)
				assert.Nil(t, result)
				return
			}

			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantAction, result.Action)
			assert.Equal(t, tt.wantContent, result.Content)
		})
	}
}

// TestSDKElicitationAdapter_NilMetaProducesNoMeta verifies that a nil domain
// Meta does not produce an _meta block on the translated request (no behavior
// change versus the prior mcp-go-only path, which never set Meta).
func TestSDKElicitationAdapter_NilMetaProducesNoMeta(t *testing.T) {
	t.Parallel()

	fake := &fakeSDKElicitationRequester{
		result: &mcp.ElicitationResult{
			ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionAccept},
		},
	}
	adapter := &sdkElicitationAdapter{mcpServer: fake}

	_, err := adapter.RequestElicitation(context.Background(), vmcp.ElicitationRequest{Message: "Confirm?"})
	require.NoError(t, err)
	assert.Nil(t, fake.gotRequest.Params.Meta)
}

// TestSDKElicitationAdapter_MetaIsCopied verifies that translating a request
// with Meta does not mutate the caller's map (NewMetaFromMap deletes
// progressToken from its argument).
func TestSDKElicitationAdapter_MetaIsCopied(t *testing.T) {
	t.Parallel()

	fake := &fakeSDKElicitationRequester{
		result: &mcp.ElicitationResult{
			ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionAccept},
		},
	}
	adapter := &sdkElicitationAdapter{mcpServer: fake}

	meta := map[string]any{"progressToken": "tok-1", "trace": "abc"}
	_, err := adapter.RequestElicitation(context.Background(), vmcp.ElicitationRequest{
		Message: "Confirm?",
		Meta:    meta,
	})
	require.NoError(t, err)

	// Caller's map is untouched.
	assert.Equal(t, "tok-1", meta["progressToken"])
	// Translated request carries the metadata.
	require.NotNil(t, fake.gotRequest.Params.Meta)
	assert.Equal(t, "tok-1", fake.gotRequest.Params.Meta.ProgressToken)
}

func TestSDKElicitationAdapter_Integration(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("test", "1.0.0")
	adapter := NewSDKElicitationAdapter(mcpServer)

	assert.NotNil(t, adapter)
}

// TestServer_MCPServer_ReturnsSameInstance verifies that (*Server).MCPServer
// returns the exact mcpcompat server pointer stored at construction time.
// Identity matters because ClientSession correlation is keyed to the server
// that received the initialize request; embedders building their own
// elicitation requester must receive the authoritative instance.
func TestServer_MCPServer_ReturnsSameInstance(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("test", "1.0.0")
	srv := &Server{mcpServer: mcpServer}

	assert.Same(t, mcpServer, srv.MCPServer())
}
