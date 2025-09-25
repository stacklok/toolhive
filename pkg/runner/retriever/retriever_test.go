package retriever

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/registry"
)

func TestGetMCPServer_WithGroup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Test group functionality by using actual registry provider
	provider, err := registry.GetDefaultProvider()
	require.NoError(t, err)

	reg, err := provider.GetRegistry()
	require.NoError(t, err)

	// Find a group that exists in the registry
	var testGroupName string
	var group *registry.Group
	for _, g := range reg.Groups {
		if g != nil && g.Name != "" {
			testGroupName = g.Name
			group = g
			break
		}
	}

	if testGroupName == "" {
		t.Skip("No groups found in registry, skipping group tests")
	}
	if group == nil {
		t.Skip("Test group is nil, skipping")
		return
	}

	// Find a server in the group to test with
	var testServerName string
	if len(group.Servers) > 0 {
		for serverName := range group.Servers {
			testServerName = serverName
			break
		}
	} else if len(group.RemoteServers) > 0 {
		for serverName := range group.RemoteServers {
			testServerName = serverName
			break
		}
	}

	if testServerName == "" {
		t.Skip("No servers found in test group, skipping")
	}

	tests := []struct {
		name          string
		serverName    string
		groupName     string
		expectError   bool
		errorContains string
	}{
		{
			name:        "valid server in group",
			serverName:  testServerName,
			groupName:   testGroupName,
			expectError: false,
		},
		{
			name:          "non-existent server in group",
			serverName:    "non-existent-server",
			groupName:     testGroupName,
			expectError:   true,
			errorContains: "not found in group",
		},
		{
			name:          "non-existent group",
			serverName:    testServerName,
			groupName:     "non-existent-group",
			expectError:   true,
			errorContains: "not found in registry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			imageURL, serverMetadata, err := GetMCPServer(
				ctx,
				tt.serverName,
				"",
				VerifyImageDisabled,
				tt.groupName,
			)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
				assert.Empty(t, imageURL)
				assert.Nil(t, serverMetadata)
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, imageURL)
				assert.NotNil(t, serverMetadata)

				// Verify server metadata name is set
				assert.Equal(t, tt.serverName, serverMetadata.GetName())
			}
		})
	}
}

func TestGetMCPServer_WithoutGroup(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Test that passing empty group name still works (normal behavior)
	imageURL, serverMetadata, err := GetMCPServer(
		ctx,
		"osv",               // Use a known server from the registry
		"",                  // rawCACertPath
		VerifyImageDisabled, // verificationType
		"",                  // empty groupName should use normal registry lookup
	)

	// This should work as it's the normal flow
	assert.NoError(t, err)
	assert.NotEmpty(t, imageURL)
	assert.NotNil(t, serverMetadata)
}

func TestResolveCACertPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		flagValue string
		expected  string
	}{
		{
			name:      "flag value provided",
			flagValue: "/path/to/ca.crt",
			expected:  "/path/to/ca.crt",
		},
		{
			name:      "empty flag value",
			flagValue: "",
			expected:  "", // Will use config or empty
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := resolveCACertPath(tt.flagValue)

			if tt.flagValue != "" {
				assert.Equal(t, tt.expected, result)
			} else {
				// When flag is empty, it uses config - we just verify it returns a string
				assert.IsType(t, "", result)
			}
		})
	}
}

func TestHasLatestTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		imageRef string
		expected bool
	}{
		{
			name:     "explicit latest tag",
			imageRef: "ubuntu:latest",
			expected: true,
		},
		{
			name:     "no tag defaults to latest",
			imageRef: "ubuntu",
			expected: true,
		},
		{
			name:     "specific tag",
			imageRef: "ubuntu:20.04",
			expected: false,
		},
		{
			name:     "digest reference",
			imageRef: "ubuntu@sha256:abcdef123456",
			expected: false,
		},
		{
			name:     "invalid reference",
			imageRef: "invalid::reference",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := hasLatestTag(tt.imageRef)
			assert.Equal(t, tt.expected, result)
		})
	}
}
