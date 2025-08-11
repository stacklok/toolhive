package runner

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/permissions"
	"github.com/stacklok/toolhive/pkg/registry"
)

func TestRunConfigBuilder_Build_WithPermissionProfile(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	invalidJSON := `{
		"read": ["file:///tmp/test-read"],
		"write": ["file:///tmp/test-write"],
		"network": "invalid-network-format"
	}`

	imageMetadata := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name: "test-image",
		},
		Permissions: &permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					InsecureAllowAll: true,
				},
			},
			Read:  []permissions.MountDeclaration{permissions.MountDeclaration("/test/read")},
			Write: []permissions.MountDeclaration{permissions.MountDeclaration("/test/write")},
		},
	}

	customProfile := &permissions.Profile{
		Network: &permissions.NetworkPermissions{
			Outbound: &permissions.OutboundNetworkPermissions{
				InsecureAllowAll: false,
				AllowHost:        []string{"localhost"},
				AllowPort:        []int{8080},
			},
		},
		Read:  []permissions.MountDeclaration{permissions.MountDeclaration("file:///tmp/test-read")},
		Write: []permissions.MountDeclaration{permissions.MountDeclaration("file:///tmp/test-write")},
	}

	curstomProfileJSON, err := json.Marshal(customProfile)
	require.NoError(t, err, "Failed to marshal custom profile to JSON")

	testCases := []struct {
		name                      string
		setupBuilder              func(*RunConfigBuilder) *RunConfigBuilder
		profileContent            string // The JSON content for the profile file
		needsTempFile             bool   // Whether this test case needs a temp file
		expectedPermissionProfile *permissions.Profile
		imageMetadata             *registry.ImageMetadata
		expectError               bool
	}{
		{
			name: "Direct permission profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfile(permissions.BuiltinNetworkProfile())
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Network profile by name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath(permissions.ProfileNetwork)
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "None profile by name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath(permissions.ProfileNone)
			},
			imageMetadata:             nil,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name: "Stdio profile by name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath("stdio")
			},
			imageMetadata:             nil,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name: "Custom profile from file",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b
			},
			profileContent:            string(curstomProfileJSON),
			needsTempFile:             true,
			imageMetadata:             nil,
			expectedPermissionProfile: customProfile,
		},
		{
			name: "Profile name overrides direct profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfile(permissions.BuiltinNoneProfile()).
					WithPermissionProfileNameOrPath(permissions.ProfileNetwork)
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Direct profile overrides profile name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath(permissions.ProfileNetwork).
					WithPermissionProfile(permissions.BuiltinNoneProfile())
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name: "Permissions from image metadata",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithName("test-container")
			},
			imageMetadata: imageMetadata,
			expectedPermissionProfile: &permissions.Profile{
				Network: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						InsecureAllowAll: true,
					},
				},
				Read:  []permissions.MountDeclaration{permissions.MountDeclaration("/test/read")},
				Write: []permissions.MountDeclaration{permissions.MountDeclaration("/test/write")},
			},
		},
		{
			name: "Defaults to network profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b
			},
			imageMetadata:             nil,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Non-existent profile file",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath("/non/existent/file.json")
			},
			imageMetadata: nil,
			expectError:   true,
		},
		{
			name: "Invalid JSON in profile file",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b
			},
			profileContent: invalidJSON,
			needsTempFile:  true,
			imageMetadata:  nil,
			expectError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a new builder and apply the setup
			builder := tc.setupBuilder(NewRunConfigBuilder())
			require.NotNil(t, builder, "Builder should not be nil")

			// Create a temporary profile file if needed
			if tc.needsTempFile {
				tempFilePath, cleanup := createTempProfileFile(t, tc.profileContent)
				defer cleanup()
				builder.WithPermissionProfileNameOrPath(tempFilePath)
			}

			// Build the configuration with required parameters
			ctx := context.Background()
			config, err := builder.Build(ctx, tc.imageMetadata, nil, mockValidator)

			if tc.expectError {
				assert.Error(t, err, "Build should return an error")
				return
			}

			require.NoError(t, err, "Build should not return an error")
			require.NotNil(t, config, "Built config should not be nil")
			require.NotNil(t, config.PermissionProfile, "Built config's PermissionProfile should not be nil")

			// Check network outbound settings
			assert.Equal(t, tc.expectedPermissionProfile.Network.Outbound.InsecureAllowAll,
				config.PermissionProfile.Network.Outbound.InsecureAllowAll,
				"Network outbound setting allow all should match in built config")

			if tc.name == "None profile by name" || tc.name == "Stdio profile by name" {
				assert.False(t, config.PermissionProfile.Network.Outbound.InsecureAllowAll,
					"None/Stdio profile should not allow all outbound network connections")
			}

			if tc.expectedPermissionProfile.Network.Outbound.AllowHost != nil {
				assert.Equal(t, tc.expectedPermissionProfile.Network.Outbound.AllowHost,
					config.PermissionProfile.Network.Outbound.AllowHost,
					"Network outbound allowed hosts should match in built config")
			}

			if tc.expectedPermissionProfile.Network.Outbound.AllowPort != nil {
				assert.Equal(t, tc.expectedPermissionProfile.Network.Outbound.AllowPort,
					config.PermissionProfile.Network.Outbound.AllowPort,
					"Network outbound allowed ports should match in built config")
			}

			// Check read/write mounts
			assert.Equal(t, len(tc.expectedPermissionProfile.Read), len(config.PermissionProfile.Read),
				"Number of read permissions should match in built config")
			assert.Equal(t, len(tc.expectedPermissionProfile.Write), len(config.PermissionProfile.Write),
				"Number of write permissions should match in built config")
		})
	}
}

func TestRunConfigBuilder_Build_WithVolumeMounts(t *testing.T) {
	t.Parallel()

	// Initialize logger to prevent nil pointer dereference when processing volume mounts
	logger.Initialize()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	testCases := []struct {
		name                string
		setupBuilder        func(*RunConfigBuilder) *RunConfigBuilder
		expectError         bool
		expectedReadMounts  int
		expectedWriteMounts int
	}{
		{
			name: "No volumes",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithVolumes([]string{})
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 0,
		},
		{
			name: "Volumes without permission profile but with profile name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithVolumes([]string{"/host:/container"}).
					WithPermissionProfileNameOrPath(permissions.ProfileNone)
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Read-only volume with existing profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithVolumes([]string{"/host:/container:ro"}).
					WithPermissionProfile(permissions.BuiltinNoneProfile())
			},
			expectError:         false,
			expectedReadMounts:  1,
			expectedWriteMounts: 0,
		},
		{
			name: "Read-write volume with existing profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithVolumes([]string{"/host:/container"}).
					WithPermissionProfile(permissions.BuiltinNoneProfile())
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Multiple volumes with existing profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithVolumes([]string{
					"/host1:/container1:ro",
					"/host2:/container2",
					"/host3:/container3:ro",
				}).WithPermissionProfile(permissions.BuiltinNoneProfile())
			},
			expectError:         false,
			expectedReadMounts:  2,
			expectedWriteMounts: 1,
		},
		{
			name: "Invalid volume format",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithVolumes([]string{"invalid:format:with:too:many:colons"}).
					WithPermissionProfile(permissions.BuiltinNoneProfile())
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a new builder and apply the setup
			builder := tc.setupBuilder(NewRunConfigBuilder())
			require.NotNil(t, builder, "Builder should not be nil")

			// Save original read/write mounts count if there's a permission profile
			var originalReadMounts, originalWriteMounts int
			if builder.config.PermissionProfile != nil {
				originalReadMounts = len(builder.config.PermissionProfile.Read)
				originalWriteMounts = len(builder.config.PermissionProfile.Write)
			}

			// Build the configuration with required parameters
			ctx := context.Background()
			config, err := builder.Build(ctx, nil, nil, mockValidator)

			if tc.expectError {
				assert.Error(t, err, "Build should return an error for invalid volume mounts")
			} else {
				assert.NoError(t, err, "Build should not return an error")
				require.NotNil(t, config, "Built config should not be nil")

				// For the "No volumes" case, we still need to check the PermissionProfile
				// because it's required by Build to succeed
				if config.PermissionProfile != nil {
					// Check read mounts
					assert.Equal(t, tc.expectedReadMounts, len(config.PermissionProfile.Read)-originalReadMounts,
						"Number of read mounts should match expected")

					// Check write mounts
					assert.Equal(t, tc.expectedWriteMounts, len(config.PermissionProfile.Write)-originalWriteMounts,
						"Number of write mounts should match expected")
				}
			}
		})
	}
}

// createTempProfileFile creates a temporary JSON profile file with the provided content
// and returns its path. The caller is responsible for removing the file using the
// returned cleanup function.
func createTempProfileFile(t *testing.T, content string) (string, func()) {
	t.Helper()

	tempFile, err := os.CreateTemp("", "profile-*.json")
	require.NoError(t, err, "Failed to create temporary file")

	_, err = tempFile.WriteString(content)
	require.NoError(t, err, "Failed to write to temporary file")
	tempFile.Close()

	cleanup := func() {
		os.Remove(tempFile.Name())
	}

	return tempFile.Name(), cleanup
}
