// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/permissions"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

const testPort = math.MaxInt16

func TestRunConfigBuilder_Build_WithPermissionProfile(t *testing.T) {
	t.Parallel()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	invalidJSON := `{
		"read": ["file:///tmp/test-read"],
		"write": ["file:///tmp/test-write"],
		"network": "invalid-network-format"
	}`

	imageMetadata := &regtypes.ImageMetadata{
		BaseServerMetadata: regtypes.BaseServerMetadata{
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
		imageMetadata             *regtypes.ImageMetadata
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

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	// Create temporary directories for volume mount source paths
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()
	tmpDir3 := t.TempDir()

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
				WithVolumes([]string{tmpDir1 + ":/container"}),
				WithPermissionProfileNameOrPath(permissions.ProfileNone),
			},
			expectError:         false,
			expectedReadMounts:  0,
			expectedWriteMounts: 1,
		},
		{
			name: "Read-only volume with existing profile",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{tmpDir1 + ":/container:ro"}),
				WithPermissionProfile(permissions.BuiltinNoneProfile()),
			},
			expectError:         false,
			expectedReadMounts:  1,
			expectedWriteMounts: 0,
		},
		{
			name: "Read-write volume with existing profile",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{tmpDir1 + ":/container"}),
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
					tmpDir1 + ":/container1:ro",
					tmpDir2 + ":/container2",
					tmpDir3 + ":/container3:ro",
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
		{
			name: "Non-existent source path",
			builderOptions: []RunConfigBuilderOption{
				WithVolumes([]string{"/nonexistent/path/that/does/not/exist:/container"}),
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

// TestAddCoreMiddlewares_TokenExchangeIntegration verifies token-exchange middleware integration and parameter propagation.
func TestAddCoreMiddlewares_TokenExchangeIntegration(t *testing.T) {
	t.Parallel()

	t.Run("token-exchange NOT added when config is nil", func(t *testing.T) {
		t.Parallel()

		var mws []types.MiddlewareConfig
		// OIDC config can be empty for this unit test since we're only testing token-exchange behavior.
		mws = addCoreMiddlewares(mws, &auth.TokenValidatorConfig{}, nil, false)

		// Expect only auth + mcp parser when token-exchange config == nil
		assert.Equal(t, auth.MiddlewareType, mws[0].Type, "first middleware should be auth")
		assert.Equal(t, mcp.ParserMiddlewareType, mws[1].Type, "second middleware should be MCP parser")

		// Ensure token-exchange type is not present in any middleware slot.
		for i, mw := range mws {
			assert.NotEqual(t, tokenexchange.MiddlewareType, mw.Type, "middleware[%d] should not be token-exchange", i)
		}
	})

	t.Run("token-exchange IS added, correctly ordered and parameters populated when config provided", func(t *testing.T) {
		t.Parallel()

		var mws []types.MiddlewareConfig
		// Provide a realistic config to ensure full parameter serialization and propagation.
		teCfg := &tokenexchange.Config{
			TokenURL:     "https://example.com/token",
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			Audience:     "test-audience",
			Scopes:       []string{"scope1", "scope2"},
			// SubjectTokenType: "", // default is access_token if empty
			HeaderStrategy: tokenexchange.HeaderStrategyReplace, // default behavior
			// ExternalTokenHeaderName not required for replace strategy
		}

		mws = addCoreMiddlewares(mws, &auth.TokenValidatorConfig{}, teCfg, false)

		// Expect auth, token-exchange, then mcp parser — verify correct order and count.
		assert.Equal(t, auth.MiddlewareType, mws[0].Type, "first middleware should be auth")
		assert.Equal(t, tokenexchange.MiddlewareType, mws[1].Type, "second middleware should be token-exchange")
		assert.Equal(t, mcp.ParserMiddlewareType, mws[2].Type, "third middleware should be MCP parser")

		// Verify the token-exchange middleware parameters are serialized and populated.
		require.NotNil(t, mws[1].Parameters, "token-exchange middleware Parameters should not be nil")
		require.NotZero(t, len(mws[1].Parameters), "token-exchange middleware Parameters should not be empty")

		// Deserialize middleware parameters and validate field propagation.
		var mwParams tokenexchange.MiddlewareParams
		err := json.Unmarshal(mws[1].Parameters, &mwParams)
		require.NoError(t, err, "unmarshal of middleware Parameters should not fail")

		require.NotNil(t, mwParams.TokenExchangeConfig, "TokenExchangeConfig in middleware params should not be nil")
		assert.Equal(t, teCfg.TokenURL, mwParams.TokenExchangeConfig.TokenURL, "TokenURL should propagate into middleware params")
		assert.Equal(t, teCfg.ClientID, mwParams.TokenExchangeConfig.ClientID, "ClientID should propagate into middleware params")
		assert.Equal(t, teCfg.ClientSecret, mwParams.TokenExchangeConfig.ClientSecret, "ClientSecret should propagate into middleware params")
		assert.Equal(t, teCfg.Audience, mwParams.TokenExchangeConfig.Audience, "Audience should propagate into middleware params")
		assert.Equal(t, teCfg.Scopes, mwParams.TokenExchangeConfig.Scopes, "Scopes should propagate into middleware params")
		assert.Equal(t, teCfg.HeaderStrategy, mwParams.TokenExchangeConfig.HeaderStrategy, "HeaderStrategy should propagate into middleware params")
	})
}

func TestRunConfigBuilder_WithToolOverride(t *testing.T) {
	t.Parallel()

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
				WithToolsOverride(tc.toolOverride),
			)

			if tc.expectError {
				assert.Nil(t, config, "Builder should be nil")
				assert.Error(t, err, "Builder should return an error")
			} else {
				assert.NotNil(t, config, "Builder should not be nil")
				assert.NoError(t, err, "Builder should not return an error")
				assert.Equal(t, tc.expectedResult, config.ToolsOverride, "Tool override should match expected")
			}
		})
	}
}

func TestRunConfigBuilder_ToolOverrideMutualExclusivity(t *testing.T) {
	t.Parallel()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	imageMetadata := &regtypes.ImageMetadata{
		BaseServerMetadata: regtypes.BaseServerMetadata{
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
			name: "Tool override map with invalid override - should error",
			builderOptions: []RunConfigBuilderOption{
				WithToolsOverride(map[string]ToolOverride{
					"tool1": {}, // Empty override (no name or description)
				}),
			},
			expectError:   true,
			errorContains: "tool override for tool1 must have either Name or Description set",
		},
		{
			name: "Valid tool override map only",
			builderOptions: []RunConfigBuilderOption{
				WithToolsOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
					"tool2": {Description: "New description"},
				}),
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

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}

	imageMetadata := &regtypes.ImageMetadata{
		BaseServerMetadata: regtypes.BaseServerMetadata{
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
				WithToolsOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
				}),
				WithToolsFilter([]string{"tool1", "tool2"}),
			},
			expectError: false,
		},
		{
			name: "Tool override with invalid tools filter",
			builderOptions: []RunConfigBuilderOption{
				WithToolsOverride(map[string]ToolOverride{
					"tool1": {Name: "renamed-tool1"},
				}),
				WithToolsFilter([]string{"tool1", "nonexistent-tool"}),
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

// TestNewOperatorRunConfigBuilder tests the NewOperatorRunConfigBuilder function
func TestNewOperatorRunConfigBuilder(t *testing.T) {
	t.Parallel()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}
	imageMetadata := &regtypes.ImageMetadata{
		BaseServerMetadata: regtypes.BaseServerMetadata{
			Name:  "test-image",
			Tools: []string{"tool1", "tool2", "tool3"},
		},
	}

	config, err := NewOperatorRunConfigBuilder(context.Background(), imageMetadata, nil, mockValidator)
	require.NoError(t, err)
	assert.NotNil(t, config, "Builder config should be initialized")
	assert.NotNil(t, config.EnvVars, "EnvVars should be initialized")
	assert.NotNil(t, config.ContainerLabels, "ContainerLabels should be initialized")
}

// TestWithEnvVars tests the WithEnvVars method
func TestWithEnvVars(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		envVars  map[string]string
		expected map[string]string
	}{
		{
			name:    "Empty env vars",
			envVars: map[string]string{},
			expected: map[string]string{
				"MCP_TRANSPORT": "stdio",
			},
		},
		{
			name: "Single env var",
			envVars: map[string]string{
				"TEST_VAR": "test_value",
			},
			expected: map[string]string{
				"MCP_TRANSPORT": "stdio",
				"TEST_VAR":      "test_value",
			},
		},
		{
			name: "Multiple env vars",
			envVars: map[string]string{
				"VAR1": "value1",
				"VAR2": "value2",
				"VAR3": "value3",
			},
			expected: map[string]string{
				"MCP_TRANSPORT": "stdio",
				"VAR1":          "value1",
				"VAR2":          "value2",
				"VAR3":          "value3",
			},
		},
		{
			name:    "Nil env vars",
			envVars: nil,
			expected: map[string]string{
				"MCP_TRANSPORT": "stdio",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock environment variable validator
			mockValidator := &mockEnvVarValidator{}
			imageMetadata := &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Name:  "test-image",
					Tools: []string{"tool1", "tool2", "tool3"},
				},
			}

			config, err := NewRunConfigBuilder(
				context.Background(),
				imageMetadata,
				nil,
				mockValidator,
				WithEnvVars(tc.envVars),
			)
			require.NoError(t, err)
			require.NotNil(t, config)

			assert.Equal(t, tc.expected, config.EnvVars, "Environment variables should match expected")
		})
	}
}

// TestWithEnvVarsOverwrite tests that WithEnvVars can overwrite existing env vars
func TestWithEnvVarsOverwrite(t *testing.T) {
	t.Parallel()

	// Create a mock environment variable validator
	mockValidator := &mockEnvVarValidator{}
	imageMetadata := &regtypes.ImageMetadata{
		BaseServerMetadata: regtypes.BaseServerMetadata{
			Name:  "test-image",
			Tools: []string{"tool1", "tool2", "tool3"},
		},
	}

	// Add initial env vars
	initialEnvVars := map[string]string{
		"EXISTING_VAR": "old_value",
		"OTHER_VAR":    "other_value",
	}

	// Add new env vars that overwrite some existing ones
	newEnvVars := map[string]string{
		"EXISTING_VAR": "new_value",
		"NEW_VAR":      "new_value",
	}

	config, err := NewRunConfigBuilder(
		context.Background(),
		imageMetadata,
		nil,
		mockValidator,
		WithEnvVars(initialEnvVars),
		WithEnvVars(newEnvVars),
	)
	require.NoError(t, err)
	require.NotNil(t, config)

	expected := map[string]string{
		"EXISTING_VAR":  "new_value",   // Should be overwritten
		"OTHER_VAR":     "other_value", // Should remain unchanged
		"NEW_VAR":       "new_value",   // Should be added
		"MCP_TRANSPORT": "stdio",
	}
	assert.Equal(t, expected, config.EnvVars, "Environment variables should be merged correctly")
}

// TestBuildForOperator tests the BuildForOperator method
func TestBuildForOperator(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name           string
		builderOptions []RunConfigBuilderOption
		expectError    bool
	}{
		{
			name: "Valid operator config with all fields",
			builderOptions: []RunConfigBuilderOption{
				WithName("test-server"),
				WithImage("test-image:latest"),
				WithTransportAndPorts("stdio", 8080, 8080),
			},
			expectError: false,
		},
		{
			name: "Valid operator config with minimal fields",
			builderOptions: []RunConfigBuilderOption{
				WithName("test-server"),
				WithImage("test-image:latest"),
			},
			expectError: false,
		},
		{
			name: "Valid operator config with env vars",
			builderOptions: []RunConfigBuilderOption{
				WithName("test-server"),
				WithImage("test-image:latest"),
				WithEnvVars(map[string]string{"TEST_VAR": "test_value"}),
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a mock environment variable validator
			mockValidator := &mockEnvVarValidator{}
			imageMetadata := &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Name:  "test-image",
					Tools: []string{"tool1", "tool2", "tool3"},
				},
			}

			config, err := NewOperatorRunConfigBuilder(
				context.Background(),
				imageMetadata,
				nil,
				mockValidator,
				tc.builderOptions...,
			)
			require.NoError(t, err)
			require.NotNil(t, config)

			if tc.expectError {
				require.Error(t, err, "BuildForOperator should return an error")
				assert.Nil(t, config, "Config should be nil on error")
			} else {
				require.NoError(t, err, "BuildForOperator should not return an error")
				assert.NotNil(t, config, "Config should not be nil on success")
			}
		})
	}
}

func TestWithEnvFileDir(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name        string
		envFileDir  string
		expectedDir string
	}{
		{
			name:        "absolute path",
			envFileDir:  "/vault/secrets",
			expectedDir: "/vault/secrets",
		},
		{
			name:        "relative path",
			envFileDir:  "./secrets",
			expectedDir: "./secrets",
		},
		{
			name:        "empty string",
			envFileDir:  "",
			expectedDir: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mockValidator := &mockEnvVarValidator{}

			config, err := NewOperatorRunConfigBuilder(
				context.Background(),
				nil,
				nil,
				mockValidator,
				WithName("test-server"),
				WithImage("test-image:latest"),
			)

			require.NoError(t, err, "Builder should not fail")
			require.NotNil(t, config, "Config should not be nil")
		})
	}
}

func TestRunConfigBuilder_WithIndividualTransportOptions(t *testing.T) {
	t.Parallel()

	mockValidator := &mockEnvVarValidator{}

	tests := []struct {
		name               string
		opts               []RunConfigBuilderOption
		expectedTransport  string
		checkPort          bool
		expectedPort       int
		checkTargetPort    bool
		expectedTargetPort int
	}{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			envVars := make(map[string]string)

			opts := append([]RunConfigBuilderOption{
				WithImage("test-image"),
				WithName("test-name"),
			}, tt.opts...)

			config, err := NewRunConfigBuilder(ctx, nil, envVars, mockValidator, opts...)
			require.NoError(t, err, "Creating RunConfig should not fail")
			require.NotNil(t, config, "RunConfig should not be nil")

			assert.Equal(t, tt.expectedTransport, string(config.Transport), "Transport should match expected value")

			if tt.checkPort {
				assert.Equal(t, tt.expectedPort, config.Port, "Port should match expected value")
			}

			if tt.checkTargetPort {
				assert.Equal(t, tt.expectedTargetPort, config.TargetPort, "TargetPort should match expected value")
			}
		})
	}
}

func TestRunConfigBuilder_WithRegistryProxyPort(t *testing.T) {
	t.Parallel()

	mockValidator := &mockEnvVarValidator{}

	tests := []struct {
		name              string
		imageMetadata     *regtypes.ImageMetadata
		cliProxyPort      int
		expectedProxyPort int
	}{
		{
			name: "uses registry proxy_port when CLI not specified",
			imageMetadata: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Name:      "test-server",
					Transport: "streamable-http",
				},
				Image:      "test-image:latest",
				ProxyPort:  testPort,
				TargetPort: testPort,
			},
			cliProxyPort:      0,
			expectedProxyPort: testPort,
		},
		{
			name: "CLI proxy_port overrides registry",
			imageMetadata: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Name:      "test-server",
					Transport: "streamable-http",
				},
				Image:      "test-image:latest",
				ProxyPort:  testPort,
				TargetPort: testPort,
			},
			cliProxyPort:      9999,
			expectedProxyPort: 9999,
		},
		{
			name: "random port when neither CLI nor registry specified",
			imageMetadata: &regtypes.ImageMetadata{
				BaseServerMetadata: regtypes.BaseServerMetadata{
					Name:      "test-server",
					Transport: "streamable-http",
				},
				Image: "test-image:latest",
			},
			cliProxyPort:      0,
			expectedProxyPort: 0, // Will be assigned randomly
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			envVars := make(map[string]string)

			opts := []RunConfigBuilderOption{
				WithImage("test-image"),
				WithName("test-name"),
				WithTransportAndPorts("streamable-http", tt.cliProxyPort, 0),
			}

			config, err := NewRunConfigBuilder(ctx, tt.imageMetadata, envVars, mockValidator, opts...)
			require.NoError(t, err, "Creating RunConfig should not fail")
			require.NotNil(t, config, "RunConfig should not be nil")

			if tt.expectedProxyPort > 0 {
				assert.Equal(t, tt.expectedProxyPort, config.Port, "ProxyPort should match expected value")
			}
		})
	}
}

// TestEmbeddedAuthServerScopePropagation verifies that the builder propagates
// EmbeddedAuthServerConfig.ScopesSupported to OIDCConfig.Scopes when no
// explicit PRM scopes are configured, and that explicit scopes are preserved.
func TestEmbeddedAuthServerScopePropagation(t *testing.T) {
	t.Parallel()

	mockValidator := &mockEnvVarValidator{}

	t.Run("propagates AS scopes to empty OIDCConfig.Scopes", func(t *testing.T) {
		t.Parallel()

		asScopes := []string{"openid", "profile", "email", "offline_access"}

		config, err := NewRunConfigBuilder(
			context.Background(),
			nil,
			nil,
			mockValidator,
			WithName("test-server"),
			WithOIDCConfig(
				"https://issuer.example.com", // issuer
				"",                           // audience
				"",                           // jwksURL
				"",                           // introspectionURL
				"",                           // clientID
				"",                           // clientSecret
				"",                           // caBundle
				"",                           // jwksAuthTokenFile
				"",                           // resourceURL
				false,                        // jwksAllowPrivateIP
				false,                        // insecureAllowHTTP
				nil,                          // scopes (empty -> should be propagated)
			),
			WithEmbeddedAuthServerConfig(&authserver.RunConfig{
				ScopesSupported: asScopes,
			}),
		)

		require.NoError(t, err, "NewRunConfigBuilder should not return an error")
		require.NotNil(t, config, "RunConfig should not be nil")
		require.NotNil(t, config.OIDCConfig, "OIDCConfig should not be nil")
		assert.Equal(t, asScopes, config.OIDCConfig.Scopes,
			"OIDCConfig.Scopes should be propagated from EmbeddedAuthServerConfig.ScopesSupported")
	})

	t.Run("does not overwrite explicit OIDCConfig.Scopes", func(t *testing.T) {
		t.Parallel()

		explicitScopes := []string{"openid", "custom-scope"}
		asScopes := []string{"openid", "profile", "email", "offline_access"}

		config, err := NewRunConfigBuilder(
			context.Background(),
			nil,
			nil,
			mockValidator,
			WithName("test-server"),
			WithOIDCConfig(
				"https://issuer.example.com", // issuer
				"",                           // audience
				"",                           // jwksURL
				"",                           // introspectionURL
				"",                           // clientID
				"",                           // clientSecret
				"",                           // caBundle
				"",                           // jwksAuthTokenFile
				"",                           // resourceURL
				false,                        // jwksAllowPrivateIP
				false,                        // insecureAllowHTTP
				explicitScopes,               // scopes (explicit -> should NOT be overwritten)
			),
			WithEmbeddedAuthServerConfig(&authserver.RunConfig{
				ScopesSupported: asScopes,
			}),
		)

		require.NoError(t, err, "NewRunConfigBuilder should not return an error")
		require.NotNil(t, config, "RunConfig should not be nil")
		require.NotNil(t, config.OIDCConfig, "OIDCConfig should not be nil")
		assert.Equal(t, explicitScopes, config.OIDCConfig.Scopes,
			"OIDCConfig.Scopes should NOT be overwritten when explicitly set")
	})

	t.Run("uses AS default scopes when EmbeddedAuthServerConfig has no ScopesSupported", func(t *testing.T) {
		t.Parallel()

		config, err := NewRunConfigBuilder(
			context.Background(),
			nil,
			nil,
			mockValidator,
			WithName("test-server"),
			WithOIDCConfig(
				"https://issuer.example.com", // issuer
				"",                           // audience
				"",                           // jwksURL
				"",                           // introspectionURL
				"",                           // clientID
				"",                           // clientSecret
				"",                           // caBundle
				"",                           // jwksAuthTokenFile
				"",                           // resourceURL
				false,                        // jwksAllowPrivateIP
				false,                        // insecureAllowHTTP
				nil,                          // scopes (empty -> should get AS defaults)
			),
			WithEmbeddedAuthServerConfig(&authserver.RunConfig{
				// ScopesSupported intentionally empty — simulates the common case
				// where the user doesn't explicitly configure scopes on the AS.
			}),
		)

		require.NoError(t, err, "NewRunConfigBuilder should not return an error")
		require.NotNil(t, config, "RunConfig should not be nil")
		require.NotNil(t, config.OIDCConfig, "OIDCConfig should not be nil")
		assert.Equal(t, registration.DefaultScopes, config.OIDCConfig.Scopes,
			"OIDCConfig.Scopes should get AS default scopes when both are unconfigured")
	})

	t.Run("no propagation when EmbeddedAuthServerConfig is nil", func(t *testing.T) {
		t.Parallel()

		config, err := NewRunConfigBuilder(
			context.Background(),
			nil,
			nil,
			mockValidator,
			WithName("test-server"),
			WithOIDCConfig(
				"https://issuer.example.com", // issuer
				"",                           // audience
				"",                           // jwksURL
				"",                           // introspectionURL
				"",                           // clientID
				"",                           // clientSecret
				"",                           // caBundle
				"",                           // jwksAuthTokenFile
				"",                           // resourceURL
				false,                        // jwksAllowPrivateIP
				false,                        // insecureAllowHTTP
				nil,                          // scopes
			),
			// No WithEmbeddedAuthServerConfig
		)

		require.NoError(t, err, "NewRunConfigBuilder should not return an error")
		require.NotNil(t, config, "RunConfig should not be nil")
		require.NotNil(t, config.OIDCConfig, "OIDCConfig should not be nil")
		assert.Empty(t, config.OIDCConfig.Scopes,
			"OIDCConfig.Scopes should remain empty when no embedded AS is configured")
	})
}
