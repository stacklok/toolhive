package server

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
)

func TestSDKElicitationAdapter_RequestElicitation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mockFunc   func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error)
		wantError  bool
		wantAction mcp.ElicitationResponseAction
	}{
		{
			name: "accept action",
			mockFunc: func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
				return &mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action:  mcp.ElicitationResponseActionAccept,
						Content: map[string]any{"confirmed": true},
					},
				}, nil
			},
			wantAction: mcp.ElicitationResponseActionAccept,
		},
		{
			name: "decline action",
			mockFunc: func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
				return &mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action: mcp.ElicitationResponseActionDecline,
					},
				}, nil
			},
			wantAction: mcp.ElicitationResponseActionDecline,
		},
		{
			name: "cancel action",
			mockFunc: func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
				return &mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action: mcp.ElicitationResponseActionCancel,
					},
				}, nil
			},
			wantAction: mcp.ElicitationResponseActionCancel,
		},
		{
			name: "SDK error",
			mockFunc: func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
				return nil, errors.New("SDK internal error")
			},
			wantError: true,
		},
		{
			name: "context cancelled",
			mockFunc: func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
				return nil, context.Canceled
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			adapter := &testSDKElicitationRequester{mockFunc: tt.mockFunc}

			request := mcp.ElicitationRequest{
				Params: mcp.ElicitationParams{
					Message:         "Test",
					RequestedSchema: map[string]any{"type": "object"},
				},
			}

			result, err := adapter.RequestElicitation(context.Background(), request)

			if tt.wantError {
				assert.Error(t, err)
				assert.Nil(t, result)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, result)
				assert.Equal(t, tt.wantAction, result.Action)
			}
		})
	}
}

func TestSDKElicitationAdapter_Integration(t *testing.T) {
	t.Parallel()

	mcpServer := server.NewMCPServer("test", "1.0.0")
	adapter := newSDKElicitationAdapter(mcpServer)

	assert.NotNil(t, adapter)
}

type testSDKElicitationRequester struct {
	mockFunc func(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error)
}

func (t *testSDKElicitationRequester) RequestElicitation(
	ctx context.Context,
	request mcp.ElicitationRequest,
) (*mcp.ElicitationResult, error) {
	if t.mockFunc != nil {
		return t.mockFunc(ctx, request)
	}
	return nil, errors.New("not implemented")
}
