package transport

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/container/runtime/mocks"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestHTTPTransport_Setup_HostNetworking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		transportType       types.TransportType
		permissionProfile   *permissions.Profile
		isolateNetwork      bool
		expectedEnvVars     map[string]string
		expectedExposedPort int
		checkFunc           func(t *testing.T, options *rt.DeployWorkloadOptions)
	}{
		{
			name:          "host networking mode - skips port bindings",
			transportType: types.TransportTypeSSE,
			permissionProfile: &permissions.Profile{
				Network: &permissions.NetworkPermissions{
					Mode: "host",
				},
			},
			isolateNetwork: false,
			expectedEnvVars: map[string]string{
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "8080",
				"FASTMCP_PORT":  "8080",
				"MCP_HOST":      "127.0.0.1",
			},
			expectedExposedPort: 9876, // Different from target port to test proper handling
			checkFunc: func(t *testing.T, options *rt.DeployWorkloadOptions) {
				t.Helper()
				// For host networking, port bindings should be empty
				assert.Empty(t, options.PortBindings, "port bindings should be empty for host networking")
				// Exposed ports should also be empty for host networking
				assert.Empty(t, options.ExposedPorts, "exposed ports should be empty for host networking")
			},
		},
		{
			name:          "bridge networking mode - sets port bindings",
			transportType: types.TransportTypeSSE,
			permissionProfile: &permissions.Profile{
				Network: &permissions.NetworkPermissions{
					Mode: "bridge",
				},
			},
			isolateNetwork: false,
			expectedEnvVars: map[string]string{
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "8080",
				"FASTMCP_PORT":  "8080",
				"MCP_HOST":      "127.0.0.1",
			},
			expectedExposedPort: 9876,
			checkFunc: func(t *testing.T, options *rt.DeployWorkloadOptions) {
				t.Helper()
				// For bridge networking, port bindings should be set
				assert.NotEmpty(t, options.PortBindings, "port bindings should not be empty for bridge networking")
				assert.Contains(t, options.PortBindings, "8080/tcp", "should have binding for port 8080")
				// Exposed ports should be set
				assert.NotEmpty(t, options.ExposedPorts, "exposed ports should not be empty for bridge networking")
				assert.Contains(t, options.ExposedPorts, "8080/tcp", "should expose port 8080")
			},
		},
		{
			name:          "default networking mode - sets port bindings",
			transportType: types.TransportTypeSSE,
			permissionProfile: &permissions.Profile{
				Network: &permissions.NetworkPermissions{
					Mode: "default",
				},
			},
			isolateNetwork: false,
			expectedEnvVars: map[string]string{
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "8080",
				"FASTMCP_PORT":  "8080",
				"MCP_HOST":      "127.0.0.1",
			},
			expectedExposedPort: 9876,
			checkFunc: func(t *testing.T, options *rt.DeployWorkloadOptions) {
				t.Helper()
				// For default networking, port bindings should be set
				assert.NotEmpty(t, options.PortBindings, "port bindings should not be empty for default networking")
				assert.Contains(t, options.PortBindings, "8080/tcp", "should have binding for port 8080")
				// Exposed ports should be set
				assert.NotEmpty(t, options.ExposedPorts, "exposed ports should not be empty for default networking")
				assert.Contains(t, options.ExposedPorts, "8080/tcp", "should expose port 8080")
			},
		},
		{
			name:          "empty network mode - sets port bindings",
			transportType: types.TransportTypeSSE,
			permissionProfile: &permissions.Profile{
				Network: nil, // No network permissions specified
			},
			isolateNetwork: false,
			expectedEnvVars: map[string]string{
				"MCP_TRANSPORT": "sse",
				"MCP_PORT":      "8080",
				"FASTMCP_PORT":  "8080",
				"MCP_HOST":      "127.0.0.1",
			},
			expectedExposedPort: 9876,
			checkFunc: func(t *testing.T, options *rt.DeployWorkloadOptions) {
				t.Helper()
				// For empty/nil networking, port bindings should be set (defaults to bridge-like behavior)
				assert.NotEmpty(t, options.PortBindings, "port bindings should not be empty for empty network mode")
				assert.Contains(t, options.PortBindings, "8080/tcp", "should have binding for port 8080")
				// Exposed ports should be set
				assert.NotEmpty(t, options.ExposedPorts, "exposed ports should not be empty for empty network mode")
				assert.Contains(t, options.ExposedPorts, "8080/tcp", "should expose port 8080")
			},
		},
		{
			name:          "host networking with streamable transport",
			transportType: types.TransportTypeStreamableHTTP,
			permissionProfile: &permissions.Profile{
				Network: &permissions.NetworkPermissions{
					Mode: "host",
				},
			},
			isolateNetwork: false,
			expectedEnvVars: map[string]string{
				"MCP_TRANSPORT": "streamable-http",
				"MCP_PORT":      "8080",
				"FASTMCP_PORT":  "8080",
				"MCP_HOST":      "127.0.0.1",
			},
			expectedExposedPort: 9876,
			checkFunc: func(t *testing.T, options *rt.DeployWorkloadOptions) {
				t.Helper()
				// For host networking, port bindings should be empty
				assert.Empty(t, options.PortBindings, "port bindings should be empty for host networking")
				assert.Empty(t, options.ExposedPorts, "exposed ports should be empty for host networking")
			},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			// Create a mock deployer
			mockDeployer := mocks.NewMockDeployer(ctrl)

			// Create HTTP transport
			transport := NewHTTPTransport(
				tt.transportType,
				"127.0.0.1", // host
				9090,        // proxyPort
				8080,        // targetPort
				mockDeployer,
				false,       // debug
				"127.0.0.1", // targetHost
				nil,         // authInfoHandler
				nil,         // prometheusHandler
			)

			// Set up expectations for DeployWorkload
			var capturedOptions *rt.DeployWorkloadOptions
			var capturedEnvVars map[string]string

			mockDeployer.EXPECT().
				DeployWorkload(
					gomock.Any(),              // ctx
					"test-image",              // image
					"test-container",          // name
					[]string{"serve"},         // command
					gomock.Any(),              // envVars - will capture
					map[string]string{},       // labels
					tt.permissionProfile,      // permissionProfile
					tt.transportType.String(), // transportType
					gomock.Any(),              // options - will capture
					tt.isolateNetwork,         // isolateNetwork
				).
				DoAndReturn(func(
					_ context.Context,
					_, _ string,
					_ []string,
					envVars, _ map[string]string,
					_ *permissions.Profile,
					_ string,
					options *rt.DeployWorkloadOptions,
					_ bool,
				) (int, error) {
					// Capture the options and envVars for inspection
					capturedOptions = options
					capturedEnvVars = envVars
					return tt.expectedExposedPort, nil
				})

			// Call Setup
			err := transport.Setup(
				context.Background(),
				mockDeployer,
				"test-container",
				"test-image",
				[]string{"serve"},
				map[string]string{}, // Initial envVars
				map[string]string{}, // labels
				tt.permissionProfile,
				"", // k8sPodTemplatePatch
				tt.isolateNetwork,
				nil, // ignoreConfig
			)

			require.NoError(t, err)

			// Verify environment variables
			for key, expectedValue := range tt.expectedEnvVars {
				assert.Equal(t, expectedValue, capturedEnvVars[key], "env var %s should match", key)
			}

			// Run custom check function
			if tt.checkFunc != nil {
				tt.checkFunc(t, capturedOptions)
			}

			// Verify targetPort is set correctly (for non-Kubernetes runtime)
			assert.Equal(t, tt.expectedExposedPort, transport.targetPort, "targetPort should be updated from exposedPort")
		})
	}
}

func TestHTTPTransport_Setup_HostNetworking_WithCustomHost(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDeployer := mocks.NewMockDeployer(ctrl)

	// Create HTTP transport with custom host
	transport := NewHTTPTransport(
		types.TransportTypeSSE,
		"192.168.1.100", // Custom host IP
		9090,            // proxyPort
		8080,            // targetPort
		mockDeployer,
		false,      // debug
		"10.0.0.1", // Custom target host
		nil,        // authInfoHandler
		nil,        // prometheusHandler
	)

	profile := &permissions.Profile{
		Network: &permissions.NetworkPermissions{
			Mode: "host",
		},
	}

	// Set up expectations
	var capturedOptions *rt.DeployWorkloadOptions
	var capturedEnvVars map[string]string

	mockDeployer.EXPECT().
		DeployWorkload(
			gomock.Any(),
			"test-image",
			"test-container",
			[]string{"serve"},
			gomock.Any(),
			map[string]string{},
			profile,
			"sse",
			gomock.Any(),
			false,
		).
		DoAndReturn(func(
			_ context.Context,
			_, _ string,
			_ []string,
			envVars, _ map[string]string,
			_ *permissions.Profile,
			_ string,
			options *rt.DeployWorkloadOptions,
			_ bool,
		) (int, error) {
			capturedOptions = options
			capturedEnvVars = envVars
			return 9876, nil
		})

	// Call Setup
	err := transport.Setup(
		context.Background(),
		mockDeployer,
		"test-container",
		"test-image",
		[]string{"serve"},
		map[string]string{},
		map[string]string{},
		profile,
		"",
		false,
		nil,
	)

	require.NoError(t, err)

	// Verify MCP_HOST uses the custom target host
	assert.Equal(t, "10.0.0.1", capturedEnvVars["MCP_HOST"], "MCP_HOST should use custom target host")

	// For host networking, port bindings should be empty even with custom host
	assert.Empty(t, capturedOptions.PortBindings, "port bindings should be empty for host networking")
	assert.Empty(t, capturedOptions.ExposedPorts, "exposed ports should be empty for host networking")
}

func TestHTTPTransport_Setup_UnsupportedTransportType(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockDeployer := mocks.NewMockDeployer(ctrl)

	// Create HTTP transport with an unsupported transport type
	// We need to create a custom transport type for this test
	unsupportedType := types.TransportType("unsupported")
	transport := &HTTPTransport{
		transportType: unsupportedType,
		host:          "127.0.0.1",
		proxyPort:     9090,
		targetPort:    8080,
		targetHost:    "127.0.0.1",
		deployer:      mockDeployer,
	}

	profile := &permissions.Profile{}

	// Call Setup - should fail due to unsupported transport type
	err := transport.Setup(
		context.Background(),
		mockDeployer,
		"test-container",
		"test-image",
		[]string{"serve"},
		map[string]string{},
		map[string]string{},
		profile,
		"",
		false,
		nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport type")
}

func TestHTTPTransport_Setup_RemoteURL(t *testing.T) {
	t.Parallel()

	// Create HTTP transport
	transport := NewHTTPTransport(
		types.TransportTypeSSE,
		"127.0.0.1",
		9090,
		8080,
		nil, // No deployer needed for remote
		false,
		"127.0.0.1",
		nil,
		nil,
	)

	// Set remote URL
	transport.SetRemoteURL("https://remote-mcp-server.com")

	// Call Setup - should succeed without calling deployer
	err := transport.Setup(
		context.Background(),
		nil, // No deployer needed
		"remote-container",
		"",  // No image for remote
		nil, // No command for remote
		map[string]string{},
		map[string]string{},
		&permissions.Profile{},
		"",
		false,
		nil,
	)

	require.NoError(t, err)
	assert.Equal(t, "remote-container", transport.containerName)
}
