package runner

import (
	"context"
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

	imageMetadata := &registry.ImageMetadata{
		Name: "test-image",
		Permissions: &permissions.Profile{
			Network: &permissions.NetworkPermissions{
				Outbound: &permissions.OutboundNetworkPermissions{
					InsecureAllowAll: true,
					AllowTransport:   []string{"http", "https"},
				},
			},
			Read:  []permissions.MountDeclaration{permissions.MountDeclaration("/test/read")},
			Write: []permissions.MountDeclaration{permissions.MountDeclaration("/test/write")},
		},
	}

	testCases := []struct {
		name                      string
		setupBuilder              func(*RunConfigBuilder) *RunConfigBuilder
		expectedPermissionProfile *permissions.Profile
		imageMetadata             *registry.ImageMetadata
	}{
		{
			name: "Build with direct permission profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfile(permissions.BuiltinNetworkProfile())
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Build with permission profile name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath(permissions.ProfileNetwork)
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Build after profile name overrides direct profile",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfile(permissions.BuiltinNoneProfile()).
					WithPermissionProfileNameOrPath(permissions.ProfileNetwork)
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNetworkProfile(),
		},
		{
			name: "Build after direct profile overrides profile name",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithPermissionProfileNameOrPath(permissions.ProfileNetwork).
					WithPermissionProfile(permissions.BuiltinNoneProfile())
			},
			imageMetadata:             imageMetadata,
			expectedPermissionProfile: permissions.BuiltinNoneProfile(),
		},
		{
			name: "Build with permissions from image metadata",
			setupBuilder: func(b *RunConfigBuilder) *RunConfigBuilder {
				return b.WithName("test-container")
			},
			imageMetadata: imageMetadata,
			expectedPermissionProfile: &permissions.Profile{
				Network: &permissions.NetworkPermissions{
					Outbound: &permissions.OutboundNetworkPermissions{
						InsecureAllowAll: true,
						AllowTransport:   []string{"http", "https"},
					},
				},
				Read:  []permissions.MountDeclaration{permissions.MountDeclaration("/test/read")},
				Write: []permissions.MountDeclaration{permissions.MountDeclaration("/test/read")},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Create a new builder and apply the setup
			builder := tc.setupBuilder(NewRunConfigBuilder())
			require.NotNil(t, builder, "Builder should not be nil")

			// Build the configuration with required parameters
			ctx := context.Background()
			config, err := builder.Build(ctx, tc.imageMetadata, nil, mockValidator)
			require.NoError(t, err, "Build should not return an error")
			require.NotNil(t, config, "Built config should not be nil")

			assert.NotNil(t, config.PermissionProfile, "Built config's PermissionProfile should not be nil")

			// Compare specific fields of the permission profile
			assert.Equal(t, tc.expectedPermissionProfile.Network.Outbound.InsecureAllowAll,
				config.PermissionProfile.Network.Outbound.InsecureAllowAll,
				"Network outbound setting allow all should match in built config")
			assert.Equal(t, tc.expectedPermissionProfile.Network.Outbound.AllowTransport,
				config.PermissionProfile.Network.Outbound.AllowTransport,
				"Network outbound setting transport should match in built config")

			assert.Equal(t, len(tc.expectedPermissionProfile.Read), len(config.PermissionProfile.Read),
				"Number of read permissions should match in built config")
			assert.Equal(t, len(tc.expectedPermissionProfile.Write), len(config.PermissionProfile.Write),
				"Number of write permissions should match in built config")
		})
	}
}
