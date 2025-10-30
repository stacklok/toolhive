package server_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
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

			s := server.New(tt.config, mockRouter, mockBackendClient)
			require.NotNil(t, s)

			// Address() returns formatted string
			addr := s.Address()
			require.Contains(t, addr, tt.expectedHost)
		})
	}
}

func TestServer_RegisterCapabilities(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successfully registers tools, resources, and prompts", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockRouter := routerMocks.NewMockRouter(ctrl)
		mockBackendClient := mocks.NewMockBackendClient(ctrl)

		// Create test capabilities
		capabilities := &aggregator.AggregatedCapabilities{
			Tools: []vmcp.Tool{
				{
					Name:        "test_tool",
					Description: "A test tool",
					InputSchema: map[string]any{
						"param1": map[string]any{
							"type": "string",
						},
					},
					BackendID: "backend1",
				},
			},
			Resources: []vmcp.Resource{
				{
					URI:         "file:///test",
					Name:        "test_resource",
					Description: "A test resource",
					MimeType:    "text/plain",
					BackendID:   "backend1",
				},
			},
			Prompts: []vmcp.Prompt{
				{
					Name:        "test_prompt",
					Description: "A test prompt",
					Arguments: []vmcp.PromptArgument{
						{
							Name:        "arg1",
							Description: "First argument",
							Required:    true,
						},
					},
					BackendID: "backend1",
				},
			},
			RoutingTable: &vmcp.RoutingTable{
				Tools: map[string]*vmcp.BackendTarget{
					"test_tool": {
						WorkloadID: "backend1",
						BaseURL:    "http://backend1:8080",
					},
				},
				Resources: map[string]*vmcp.BackendTarget{
					"file:///test": {
						WorkloadID: "backend1",
						BaseURL:    "http://backend1:8080",
					},
				},
				Prompts: map[string]*vmcp.BackendTarget{
					"test_prompt": {
						WorkloadID: "backend1",
						BaseURL:    "http://backend1:8080",
					},
				},
			},
		}

		// Expect router update
		mockRouter.EXPECT().
			UpdateRoutingTable(gomock.Any(), capabilities.RoutingTable).
			Return(nil)

		s := server.New(&server.Config{}, mockRouter, mockBackendClient)
		err := s.RegisterCapabilities(ctx, capabilities)
		require.NoError(t, err)
	})

	t.Run("fails when routing table update fails", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockRouter := routerMocks.NewMockRouter(ctrl)
		mockBackendClient := mocks.NewMockBackendClient(ctrl)

		capabilities := &aggregator.AggregatedCapabilities{
			Tools:        []vmcp.Tool{},
			Resources:    []vmcp.Resource{},
			Prompts:      []vmcp.Prompt{},
			RoutingTable: &vmcp.RoutingTable{},
		}

		// Expect router update to fail
		mockRouter.EXPECT().
			UpdateRoutingTable(gomock.Any(), gomock.Any()).
			Return(assert.AnError)

		s := server.New(&server.Config{}, mockRouter, mockBackendClient)
		err := s.RegisterCapabilities(ctx, capabilities)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to update routing table")
	})

	t.Run("fails when starting without registered capabilities", func(t *testing.T) {
		t.Parallel()

		ctrl := gomock.NewController(t)
		t.Cleanup(ctrl.Finish)

		mockRouter := routerMocks.NewMockRouter(ctrl)
		mockBackendClient := mocks.NewMockBackendClient(ctrl)

		s := server.New(&server.Config{}, mockRouter, mockBackendClient)

		// Create a context that we can cancel immediately
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately to prevent actual server start

		err := s.Start(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "capabilities not registered")
	})
}

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

			s := server.New(tt.config, mockRouter, mockBackendClient)
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

		s := server.New(&server.Config{}, mockRouter, mockBackendClient)
		err := s.Stop(context.Background())
		require.NoError(t, err)
	})
}
