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
		builderOptions            []RunConfigBuilderOption
		profileContent            string // The JSON content for the profile file
		needsTempFile             bool   // Whether this test case needs a temp file
		expectedPermissionProfile *permissions.Profile
		imageMetadata             *registry.ImageMetadata
		expectError               bool
	}{
		{
			name: "Direct permission profile",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfile(permissions.BuiltinNetworkProfile()),
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Network profile by name",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfileNameOrPath(permissions.ProfileNetwork),
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "None profile by name",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfileNameOrPath(permissions.ProfileNone),
			},
			imageMetadata:             nil,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name: "Stdio profile by name",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfileNameOrPath("stdio"),
			},
			imageMetadata:             nil,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name:                      "Custom profile from file",
			builderOptions:            []RunConfigBuilderOption{},
			profileContent:            string(curstomProfileJSON),
			needsTempFile:             true,
			imageMetadata:             nil,
			expectedPermissionProfile: customProfile,
		},
		{
			name: "Profile name overrides direct profile",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
				WithPermissionProfileNameOrPath(permissions.ProfileNetwork),
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Direct profile overrides profile name",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfileNameOrPath(permissions.ProfileNetwork),
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name: "Permissions from image metadata",
			builderOptions: []RunConfigBuilderOption{
				WithName("test-container"),
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
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfileNameOrPath(permissions.ProfileNetwork),
			},
			imageMetadata:             nil,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Non-existent profile file",
			builderOptions: []RunConfigBuilderOption{
				WithPermissionProfileNameOrPath("/non/existent/file.json"),
			},
			imageMetadata: nil,
			expectError:   true,
		},
		{
			name:           "Invalid JSON in profile file",
			builderOptions: []RunConfigBuilderOption{},
			profileContent: invalidJSON,
			needsTempFile:  true,
			imageMetadata:  nil,
			expectError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			opts := tc.builderOptions

			// Create a temporary profile file if needed
			if tc.needsTempFile {
				tempFilePath, cleanup := createTempProfileFile(t, tc.profileContent)
				defer cleanup()
				opts = append(opts, WithPermissionProfileNameOrPath(tempFilePath))
			}

			ctx := context.Background()
			config, err := NewRunConfigBuilder(
				ctx,
				tc.imageMetadata,
				nil,
				mockValidator,
				opts...,
			)

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
		builderOptions      []RunConfigBuilderOption
		expectError         bool
		expectedReadMounts  int
		expectedWriteMounts int
	}{
		{
			name: "No volumes",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{}),
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 0,
		},
		{
			name: "Volumes without permission profile but with profile name",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{"/host:/container"}),
				WithPermissionProfileNameOrPath(permissions.ProfileNone),
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Read-only volume with existing profile",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{"/host:/container:ro"}),
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
			},
			expectError:         false,
			expectedReadMounts:  1,
			expectedWriteMounts: 0,
		},
		{
			name: "Read-write volume with existing profile",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{"/host:/container"}),
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Multiple volumes with existing profile",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{
					"/host1:/container1:ro",
					"/host2:/container2",
					"/host3:/container3:ro",
				}),
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
			},
			expectError:         false,
			expectedReadMounts:  2,
			expectedWriteMounts: 1,
		},
		{
			name: "Invalid volume format",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{"invalid:format:with:too:many:colons"}),
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
			},
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			config, err := NewRunConfigBuilder(
				ctx,
				nil,
				nil,
				mockValidator,
				tc.builderOptions...,
			)

			if tc.expectError {
				assert.Nil(t, config, "Builder should be nil")
				assert.Error(t, err, "Build should return an error for invalid volume mounts")
			} else {
				assert.NoError(t, err, "Build should not return an error")
				require.NotNil(t, config, "Built config should not be nil")

				// For the "No volumes" case, we still need to check the PermissionProfile
				// because it's required by Build to succeed
				if config.PermissionProfile != nil {
					// Check read mounts
					assert.Equal(t, tc.expectedReadMounts, len(config.PermissionProfile.Read),
						"Number of read mounts should match expected")

					// Check write mounts
					assert.Equal(t, tc.expectedWriteMounts, len(config.PermissionProfile.Write),
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

func TestRunConfigBuilder_WithToolOverride(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	testCases := []struct {
		name           string
		toolOverride   map[string]ToolOverride
		expectedResult map[string]ToolOverride
		expectError    bool
	}{
		{
			name: "Valid tool override with name",
			toolOverride: map[string]ToolOverride{
				"test-tool": {
					Name: "renamed-tool",
				},
			},
			expectedResult: map[string]ToolOverride{
				"test-tool": {
					Name: "renamed-tool",
				},
			},
			expectError: false,
		},
		{
			name: "Valid tool override with description",
			toolOverride: map[string]ToolOverride{
				"test-tool": {
					Description: "New description",
				},
			},
			expectedResult: map[string]ToolOverride{
				"test-tool": {
					Description: "New description",
				},
			},
			expectError: false,
		},
		{
			name: "Valid tool override with both name and description",
			toolOverride: map[string]ToolOverride{
				"test-tool": {
					Name:        "renamed-tool",
					Description: "New description",
				},
			},
			expectedResult: map[string]ToolOverride{
				"test-tool": {
					Name:        "renamed-tool",
					Description: "New description",
				},
			},
			expectError: false,
		},
		{
			name: "Multiple tool overrides",
			toolOverride: map[string]ToolOverride{
				"tool1": {
					Name: "renamed-tool1",
				},
				"tool2": {
					Description: "New description for tool2",
				},
			},
			expectedResult: map[string]ToolOverride{
				"tool1": {
					Name: "renamed-tool1",
				},
				"tool2": {
					Description: "New description for tool2",
				},
			},
			expectError: false,
		},
		{
			name:           "Empty tool override map",
			toolOverride:   map[string]ToolOverride{},
			expectedResult: map[string]ToolOverride{},
			expectError:    false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config, err := NewRunConfigBuilder(
				context.Background(),
				nil,
				nil,
				mockValidator,
				WithToolOverride(tc.toolOverride),
			)

			if tc.expectError {
				assert.Nil(t, config, "Builder should be nil")
				assert.Error(t, err, "Builder should return an error")
			} else {
				assert.NotNil(t, config, "Builder should not be nil")
				assert.NoError(t, err, "Builder should not return an error")
				assert.Equal(t, tc.expectedResult, config.ToolOverride, "Tool override should match expected")
			}
		})
	}
}

func TestRunConfigBuilder_WithToolOverrideFile(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	testCases := []struct {
		name             string
		toolOverrideFile string
		expectedResult   string
		expectError      bool
	}{
		{
			name:             "Valid tool override file path",
			toolOverrideFile: "/path/to/tool-override.json",
			expectedResult:   "/path/to/tool-override.json",
			expectError:      false,
		},
		{
			name:             "Empty tool override file path",
			toolOverrideFile: "",
			expectedResult:   "",
			expectError:      false,
		},
		{
			name:             "Relative tool override file path",
			toolOverrideFile: "./tool-override.json",
			expectedResult:   "./tool-override.json",
			expectError:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			config, err := NewRunConfigBuilder(
				context.Background(),
				nil,
				nil,
				mockValidator,
				WithToolOverrideFile(tc.toolOverrideFile),
			)

			if tc.expectError {
				assert.Nil(t, config, "Builder should be nil")
				assert.Error(t, err, "Builder should return an error")
			} else {
				assert.NotNil(t, config, "Builder should not be nil")
				assert.NoError(t, err, "Builder should not return an error")
				assert.Equal(t, tc.expectedResult, config.ToolOverrideFile, "Tool override file should match expected")
			}
		})
	}
}

func TestRunConfigBuilder_ToolOverrideMutualExclusivity(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	imageMetadata := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:  "test-image",
			Tools: []string{"tool1", "tool2", "tool3"},
		},
	}

	testCases := []struct {
		name           string
		builderOptions []RunConfigBuilderOption
		expectError    bool
		errorContains  string
	}{
		{
			name: "Both tool override map and file set - should error",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
				}),
				WithToolOverrideFile("/path/to/override.json"),
			},
			expectError:   true,
			errorContains: "both tool override map and tool override file are set, they are mutually exclusive",
		},
		{
			name: "Tool override map with invalid override - should error",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverride(map[string]ToolOverride{
					"tool1": {}, // Empty override (no name or description)
				}),
			},
			expectError:   true,
			errorContains: "tool override for tool1 must have either Name or Description set",
		},
		{
			name: "Valid tool override map only",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
					"tool2": {Description: "New description"},
				}),
			},
			expectError: false,
		},
		{
			name: "Valid tool override file only",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverrideFile("/path/to/override.json"),
			},
			expectError: false,
		},
		{
			name: "Neither tool override map nor file set",
			builderOptions: []RunConfigBuilderOption{
				WithName("test-server"),
				WithImage("test-image"),
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			config, err := NewRunConfigBuilder(
				ctx,
				imageMetadata,
				nil,
				mockValidator,
				tc.builderOptions...,
			)

			if tc.expectError {
				assert.Nil(t, config, "Builder should be nil")
				assert.Error(t, err, "Build should return an error")
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains, "Error should contain expected message")
				}
			} else {
				assert.NotNil(t, config, "Builder should not be nil")
				assert.NoError(t, err, "Builder should not return an error")
			}
		})
	}
}

func TestRunConfigBuilder_ToolOverrideWithToolsFilter(t *testing.T) {
	t.Parallel()

	// Needed to prevent a nil pointer dereference in the logger.
	logger.Initialize()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	imageMetadata := &registry.ImageMetadata{
		BaseServerMetadata: registry.BaseServerMetadata{
			Name:  "test-image",
			Tools: []string{"tool1", "tool2", "tool3"},
		},
	}

	testCases := []struct {
		name           string
		builderOptions []RunConfigBuilderOption
		expectError    bool
	}{
		{
			name: "Tool override with valid tools filter",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
				}),
				WithToolsFilter([]string{"tool1", "tool2"}),
			},
			expectError: false,
		},
		{
			name: "Tool override with invalid tools filter",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
				}),
				WithToolsFilter([]string{"tool1", "nonexistent-tool"}),
			},
			expectError: true,
		},
		{
			name: "Tool override file with valid tools filter",
			builderOptions: []RunConfigBuilderOption{
				WithToolOverrideFile("/path/to/override.json"),
				WithToolsFilter([]string{"tool1", "tool2"}),
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			config, err := NewRunConfigBuilder(
				ctx,
				imageMetadata,
				nil,
				mockValidator,
				tc.builderOptions...,
			)

			if tc.expectError {
				assert.Nil(t, config, "Builder should be nil")
				assert.Error(t, err, "Build should return an error")
			} else {
				assert.NotNil(t, config, "Builder should not be nil")
				assert.NoError(t, err, "Build should not return an error")
			}
		})
	}
}
