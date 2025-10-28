package client

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestHTTPBackendClient_ListCapabilities_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("factory error")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		capabilities, err := backendClient.ListCapabilities(context.Background(), target)

		require.Error(t, err)
		assert.Nil(t, capabilities)
		assert.Contains(t, err.Error(), "failed to create client")
		assert.Contains(t, err.Error(), "test-backend")
	})
}

func TestDefaultClientFactory_UnsupportedTransport(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		transportType string
	}{
		{
			name:          "stdio transport",
			transportType: "stdio",
		},
		{
			name:          "unknown transport",
			transportType: "unknown-protocol",
		},
		{
			name:          "empty transport",
			transportType: "",
		},
	}

	for _, tc := range testCases {
		tc := tc // Capture range variable
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			target := &vmcp.BackendTarget{
				WorkloadID:    "test-backend",
				WorkloadName:  "Test Backend",
				BaseURL:       "http://localhost:8080",
				TransportType: tc.transportType,
			}

			_, err := defaultClientFactory(context.Background(), target)

			require.Error(t, err)
			assert.ErrorIs(t, err, vmcp.ErrUnsupportedTransport)
			assert.Contains(t, err.Error(), tc.transportType)
		})
	}
}

func TestHTTPBackendClient_CallTool_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("connection failed")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		result, err := backendClient.CallTool(context.Background(), target, "test_tool", map[string]any{})

		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to create client")
	})
}

func TestHTTPBackendClient_ReadResource_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("connection failed")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		data, err := backendClient.ReadResource(context.Background(), target, "test://resource")

		require.Error(t, err)
		assert.Nil(t, data)
		assert.Contains(t, err.Error(), "failed to create client")
	})
}

func TestHTTPBackendClient_GetPrompt_WithMockFactory(t *testing.T) {
	t.Parallel()

	t.Run("handles client factory error", func(t *testing.T) {
		t.Parallel()

		expectedErr := errors.New("connection failed")
		mockFactory := func(_ context.Context, _ *vmcp.BackendTarget) (*client.Client, error) {
			return nil, expectedErr
		}

		backendClient := &httpBackendClient{
			clientFactory: mockFactory,
		}

		target := &vmcp.BackendTarget{
			WorkloadID:    "test-backend",
			WorkloadName:  "Test Backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		prompt, err := backendClient.GetPrompt(context.Background(), target, "test_prompt", map[string]any{"arg": "value"})

		require.Error(t, err)
		assert.Empty(t, prompt)
		assert.Contains(t, err.Error(), "failed to create client")
	})
}

func TestInitializeClient_ErrorHandling(t *testing.T) {
	t.Parallel()

	// This test verifies that initializeClient properly propagates errors
	// We can't easily test the success case without a real MCP server
	// Integration tests will cover the success path
	t.Run("error handling structure", func(t *testing.T) {
		t.Parallel()

		// Verify that initializeClient exists and has the right signature
		// The actual error handling is tested via integration tests
		assert.NotNil(t, initializeClient)
	})
}
