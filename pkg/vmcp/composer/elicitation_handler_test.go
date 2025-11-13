package composer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp/composer/mocks"
)

func TestDefaultElicitationHandler_RequestElicitation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		config      *ElicitationConfig
		mockSetup   func(*mocks.MockSDKElicitationRequester)
		wantErr     bool
		errType     error
		errContains string
		wantAction  string
	}{
		{
			name: "success_accept",
			config: &ElicitationConfig{
				Message: "Confirm action?",
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"confirmed": map[string]any{"type": "boolean"},
					},
				},
				Timeout: 1 * time.Minute,
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action:  mcp.ElicitationResponseActionAccept,
						Content: map[string]any{"confirmed": true},
					},
				}, nil)
			},
			wantErr:    false,
			wantAction: "accept",
		},
		{
			name: "success_decline",
			config: &ElicitationConfig{
				Message: "Proceed?",
				Schema:  map[string]any{"type": "object"},
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action: mcp.ElicitationResponseActionDecline,
					},
				}, nil)
			},
			wantErr:    false,
			wantAction: "decline",
		},
		{
			name: "success_cancel",
			config: &ElicitationConfig{
				Message: "Continue?",
				Schema:  map[string]any{"type": "object"},
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action: mcp.ElicitationResponseActionCancel,
					},
				}, nil)
			},
			wantErr:    false,
			wantAction: "cancel",
		},
		{
			name:        "nil_config",
			config:      nil,
			mockSetup:   func(_ *mocks.MockSDKElicitationRequester) {},
			wantErr:     true,
			errContains: "elicitation config cannot be nil",
		},
		{
			name: "missing_message",
			config: &ElicitationConfig{
				Schema: map[string]any{"type": "object"},
			},
			mockSetup:   func(_ *mocks.MockSDKElicitationRequester) {},
			wantErr:     true,
			errContains: "elicitation message is required",
		},
		{
			name: "missing_schema",
			config: &ElicitationConfig{
				Message: "Confirm?",
			},
			mockSetup:   func(_ *mocks.MockSDKElicitationRequester) {},
			wantErr:     true,
			errContains: "elicitation schema is required",
		},
		{
			name: "sdk_error",
			config: &ElicitationConfig{
				Message: "Confirm?",
				Schema:  map[string]any{"type": "object"},
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(nil, errors.New("network error"))
			},
			wantErr:     true,
			errContains: "elicitation request failed",
		},
		{
			name: "timeout_via_context",
			config: &ElicitationConfig{
				Message: "Confirm?",
				Schema:  map[string]any{"type": "object"},
				Timeout: 100 * time.Millisecond,
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(nil, context.DeadlineExceeded)
			},
			wantErr: true,
			errType: ErrElicitationTimeout,
		},
		{
			name: "timeout_capped_to_max",
			config: &ElicitationConfig{
				Message: "Confirm?",
				Schema:  map[string]any{"type": "object"},
				Timeout: 1 * time.Hour, // Exceeds max (10 minutes)
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				// Mock should be called with 10 minute timeout context
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action: mcp.ElicitationResponseActionAccept,
					},
				}, nil)
			},
			wantErr:    false,
			wantAction: "accept",
		},
		{
			name: "schema_too_large",
			config: &ElicitationConfig{
				Message: "Confirm?",
				Schema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"large_field": map[string]any{
							"type":        "string",
							"description": strings.Repeat("A", 200*1024), // 200KB > 100KB limit
						},
					},
				},
			},
			mockSetup:   func(_ *mocks.MockSDKElicitationRequester) {},
			wantErr:     true,
			errType:     ErrSchemaTooLarge,
			errContains: "schema too large",
		},
		{
			name: "content_too_large",
			config: &ElicitationConfig{
				Message: "Confirm?",
				Schema:  map[string]any{"type": "object"},
			},
			mockSetup: func(m *mocks.MockSDKElicitationRequester) {
				m.EXPECT().RequestElicitation(gomock.Any(), gomock.Any()).Return(&mcp.ElicitationResult{
					ElicitationResponse: mcp.ElicitationResponse{
						Action:  mcp.ElicitationResponseActionAccept,
						Content: map[string]any{"huge": strings.Repeat("A", 2*1024*1024)}, // 2MB
					},
				}, nil)
			},
			wantErr: true,
			errType: ErrContentTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			mockSDK := mocks.NewMockSDKElicitationRequester(ctrl)
			tt.mockSetup(mockSDK)

			handler := NewDefaultElicitationHandler(mockSDK)

			ctx := context.Background()
			response, err := handler.RequestElicitation(ctx, "workflow-1", "step-1", tt.config)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errType != nil {
					assert.ErrorIs(t, err, tt.errType)
				}
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, response)
				if tt.wantAction != "" {
					assert.Equal(t, tt.wantAction, response.Action)
				}
			}
		})
	}
}

func TestValidateSchemaSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  map[string]any
		wantErr bool
		errType error
	}{
		{
			name: "valid_simple_schema",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string"},
				},
			},
			wantErr: false,
		},
		{
			name: "schema_too_large",
			schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"huge": map[string]any{
						"type":        "string",
						"description": strings.Repeat("A", 200*1024),
					},
				},
			},
			wantErr: true,
			errType: ErrSchemaTooLarge,
		},
		{
			name: "schema_too_deep",
			schema: func() map[string]any {
				// Create deeply nested schema (20 levels > 10 max)
				s := map[string]any{"type": "string"}
				for i := 0; i < 20; i++ {
					s = map[string]any{
						"properties": map[string]any{
							"nested": s,
						},
					}
				}
				return s
			}(),
			wantErr: true,
			errType: ErrSchemaTooDeep,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSchemaSize(tt.schema)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errType != nil {
					assert.ErrorIs(t, err, tt.errType)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateContentSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content any
		wantErr bool
		errType error
	}{
		{
			name:    "nil_content",
			content: nil,
			wantErr: false,
		},
		{
			name: "small_content",
			content: map[string]any{
				"data": "test",
			},
			wantErr: false,
		},
		{
			name: "content_too_large",
			content: map[string]any{
				"huge": strings.Repeat("A", 2*1024*1024), // 2MB > 1MB limit
			},
			wantErr: true,
			errType: ErrContentTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateContentSize(tt.content)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errType != nil {
					assert.ErrorIs(t, err, tt.errType)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
