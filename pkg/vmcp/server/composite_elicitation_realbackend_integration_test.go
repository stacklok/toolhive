// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/client"
	"github.com/stacklok/toolhive-core/mcpcompat/client/transport"
	mcpmcp "github.com/stacklok/toolhive-core/mcpcompat/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp/composer"
)

// gatedCompositeTool is a composite tool whose only backend call ("echo" on the
// real backend) sits behind an elicitation gate that skips the remaining steps
// when the user declines or cancels.
const gatedCompositeTool = "gated_deploy"

// gatedCompositeWorkflowDefs returns a workflow with an elicitation gate
// (onDecline/onCancel: skip_remaining) followed by a tool step that carries no
// condition. Only the gate's skip_remaining action can keep the tool step from
// running.
func gatedCompositeWorkflowDefs() map[string]*composer.WorkflowDefinition {
	skipRemaining := &composer.ElicitationHandler{Action: "skip_remaining"}
	return map[string]*composer.WorkflowDefinition{
		gatedCompositeTool: {
			Name:        gatedCompositeTool,
			Description: "deploy after the user confirms",
			Steps: []composer.WorkflowStep{
				{
					ID:   "confirm",
					Type: composer.StepTypeElicitation,
					Elicitation: &composer.ElicitationConfig{
						Message: "Proceed with deployment?",
						Schema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"confirmed": map[string]any{"type": "boolean"},
							},
						},
						// Below forwardingRealBackendTimeout so a lost elicitation
						// fails the call rather than the test deadline.
						Timeout:   30 * time.Second,
						OnDecline: skipRemaining,
						OnCancel:  skipRemaining,
					},
				},
				{
					ID:        "deploy",
					Type:      composer.StepTypeTool,
					Tool:      "echo",
					DependsOn: []string{"confirm"},
					Arguments: map[string]any{"input": "deployed"},
				},
			},
		},
	}
}

// newElicitationGateClient connects a downstream client to vmcpURL that answers
// every elicitation with action, and returns it with a channel that receives
// once the elicitation handler has fired. The client is pinned to the Legacy
// revision and keeps the standalone stream open: server-initiated elicitation
// exists only on a Legacy session (see legacyPinningRoundTripper).
func newElicitationGateClient(
	ctx context.Context, t *testing.T, vmcpURL string, action mcpmcp.ElicitationResponseAction,
) (*client.Client, <-chan struct{}) {
	t.Helper()

	elicited := make(chan struct{}, 1)
	handler := client.ElicitationHandlerFunc(
		func(_ context.Context, _ mcpmcp.ElicitationRequest) (*mcpmcp.ElicitationResult, error) {
			select {
			case elicited <- struct{}{}:
			default:
			}
			resp := mcpmcp.ElicitationResponse{Action: action}
			if action == mcpmcp.ElicitationResponseActionAccept {
				resp.Content = map[string]any{"confirmed": true}
			}
			return &mcpmcp.ElicitationResult{ElicitationResponse: resp}, nil
		},
	)

	hc, pinRT := newLegacyPinnedHTTPClient()
	c, err := client.NewStreamableHttpClientWithOpts(
		vmcpURL,
		[]transport.StreamableHTTPCOption{
			transport.WithHTTPBasicClient(hc),
			transport.WithContinuousListening(),
		},
		[]client.ClientOption{client.WithElicitationHandler(handler)},
	)
	require.NoError(t, err)
	require.NoError(t, c.Start(ctx))
	t.Cleanup(func() { _ = c.Close() })

	_, err = c.Initialize(ctx, mcpmcp.InitializeRequest{
		Params: mcpmcp.InitializeParams{
			ProtocolVersion: mcpmcp.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpmcp.Implementation{Name: "gate-client", Version: "1.0"},
		},
	})
	require.NoError(t, err)
	requirePinFired(t, pinRT)

	return c, elicited
}

// TestIntegration_CompositeElicitationGate_RealBackend drives a composite tool
// end to end: a real downstream client calls it through vMCP, vMCP elicits that
// client, and the gated step routes to a real backend whose tool counts its
// invocations. When the user declines or cancels a skip_remaining gate, the
// backend tool must never be invoked and the composite call must still succeed;
// when the user accepts, the backend tool runs exactly once.
//
// Regression test for GHSA-hjq9-7gq7-wg83.
func TestIntegration_CompositeElicitationGate_RealBackend(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		action          mcpmcp.ElicitationResponseAction
		wantBackendCall bool
	}{
		{
			name:            "decline_skips_gated_backend_tool",
			action:          mcpmcp.ElicitationResponseActionDecline,
			wantBackendCall: false,
		},
		{
			name:            "cancel_skips_gated_backend_tool",
			action:          mcpmcp.ElicitationResponseActionCancel,
			wantBackendCall: false,
		},
		{
			name:            "accept_runs_gated_backend_tool",
			action:          mcpmcp.ElicitationResponseActionAccept,
			wantBackendCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), forwardingRealBackendTimeout)
			t.Cleanup(cancel)

			var backendCalls atomic.Int32
			backendURL := startRealMCPBackendWithToolDelay(t, 0, func() { backendCalls.Add(1) })
			vmcpTS := httptest.NewServer(
				newRealTestHandlerWithWorkflows(t, backendURL, gatedCompositeWorkflowDefs()))
			t.Cleanup(vmcpTS.Close)

			c, elicited := newElicitationGateClient(ctx, t, vmcpTS.URL+"/mcp", tt.action)

			res, err := c.CallTool(ctx, mcpmcp.CallToolRequest{
				Params: mcpmcp.CallToolParams{Name: gatedCompositeTool, Arguments: map[string]any{}},
			})
			require.NoError(t, err)
			require.NotNil(t, res)
			require.False(t, res.IsError, "composite call must succeed: %+v", res.Content)

			// The gate must have reached the real downstream client.
			select {
			case <-elicited:
			case <-ctx.Done():
				t.Fatalf("downstream elicitation handler was not invoked: %v", ctx.Err())
			}

			require.Len(t, res.Content, 1)
			txt, ok := mcpmcp.AsTextContent(res.Content[0])
			require.True(t, ok, "composite result must be text content")

			if tt.wantBackendCall {
				assert.Equal(t, int32(1), backendCalls.Load(),
					"accepted gate must let the backend tool run once")
				assert.Contains(t, txt.Text, "deployed")
				return
			}
			assert.Zero(t, backendCalls.Load(),
				"gated backend tool must not run after the user answers %q", tt.action)
			assert.Contains(t, txt.Text, `"skipped":true`,
				"composite result must report the skipped workflow")
		})
	}
}
