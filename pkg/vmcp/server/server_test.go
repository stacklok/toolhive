package server_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	discoveryMocks "github.com/stacklok/toolhive/pkg/vmcp/discovery/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/mocks"
	routerMocks "github.com/stacklok/toolhive/pkg/vmcp/router/mocks"
	"github.com/stacklok/toolhive/pkg/vmcp/server"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		config       *server.Config
		expectedHost string
		expectedPort int
		expectedPath string
		expectedName string
		expectedVer  string
	}{
		{
			name:         "applies all defaults",
			config:       &server.Config{},
			expectedHost: "127.0.0.1",
			expectedPort: 4483,
			expectedPath: "/mcp",
			expectedName: "toolhive-vmcp",
			expectedVer:  "0.1.0",
		},
		{
			name: "uses provided configuration",
			config: &server.Config{
				Name:         "custom-vmcp",
				Version:      "1.0.0",
				Host:         "0.0.0.0",
				Port:         8080,
				EndpointPath: "/api/mcp",
			},
			expectedHost: "0.0.0.0",
			expectedPort: 8080,
			expectedPath: "/api/mcp",
			expectedName: "custom-vmcp",
			expectedVer:  "1.0.0",
		},
		{
			name: "applies partial defaults",
			config: &server.Config{
				Host: "192.168.1.1",
				Port: 9000,
			},
			expectedHost: "192.168.1.1",
			expectedPort: 9000,
			expectedPath: "/mcp",
			expectedName: "toolhive-vmcp",
			expectedVer:  "0.1.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			s, err := server.New(context.Background(), tt.config, mockRouter, mockBackendClient, mockDiscoveryMgr, []vmcp.Backend{}, nil)
			require.NoError(t, err)
			require.NotNil(t, s)

			// Address() returns formatted string
			addr := s.Address()
			require.Contains(t, addr, tt.expectedHost)
		})
	}
}

// TestServer_RegisterCapabilities has been removed because with lazy discovery,
// capabilities are no longer registered upfront. Instead, they are discovered
// per-user via the discovery middleware when requests are made.

func TestServer_Address(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *server.Config
		expected string
	}{
		{
			name:     "default configuration",
			config:   &server.Config{},
			expected: "127.0.0.1:4483",
		},
		{
			name: "custom host and port",
			config: &server.Config{
				Host: "0.0.0.0",
				Port: 8080,
			},
			expected: "0.0.0.0:8080",
		},
		{
			name: "localhost",
			config: &server.Config{
				Host: "localhost",
				Port: 3000,
			},
			expected: "localhost:3000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			t.Cleanup(ctrl.Finish)

			mockRouter := routerMocks.NewMockRouter(ctrl)
			mockBackendClient := mocks.NewMockBackendClient(ctrl)
			mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)

			s, err := server.New(context.Background(), tt.config, mockRouter, mockBackendClient, mockDiscoveryMgr, []vmcp.Backend{}, nil)
			require.NoError(t, err)
			addr := s.Address()
			assert.Equal(t, tt.expected, addr)
		})
	}
}

func TestServer_Stop(t *testing.T) {
	t.Parallel()

	t.Run("stop without starting is safe", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockRouter := routerMocks.NewMockRouter(ctrl)
		mockBackendClient := mocks.NewMockBackendClient(ctrl)
		mockDiscoveryMgr := discoveryMocks.NewMockManager(ctrl)
		mockDiscoveryMgr.EXPECT().Stop().Times(1)

		s, err := server.New(context.Background(), &server.Config{}, mockRouter, mockBackendClient, mockDiscoveryMgr, []vmcp.Backend{}, nil)
		require.NoError(t, err)
		err = s.Stop(context.Background())
		require.NoError(t, err)
	})
}

// TestServer_ToolSchemaConversion verifies that tool InputSchema is correctly
// converted to MCP format without double-nesting.
//
// This test addresses a bug where InputSchema (which is already a complete
// JSON Schema with type, properties, required) was being wrapped in a new
// ToolInputSchema struct, causing an extra layer of nesting.
//
// Example of the bug:
// Input:  {type: "object", properties: {...}, required: [...]}
// Output: {type: "object", properties: {properties: {...}, required: [...], type: "object"}}
//
// The fix uses RawInputSchema to pass the schema as-is without double-nesting.
// TestServer_ToolSchemaConversion has been removed because with lazy discovery,
// capabilities (including tool schemas) are discovered per-user via the discovery
// middleware when requests are made. Schema validation now happens in the
// aggregator and is tested there.
