//go:build !windows
// +build !windows

package sdk

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
)

func init() {
	// Initialize the logger for tests
	logger.Initialize()
}

func TestColimaRuntimeTypeSupport(t *testing.T) {
	t.Parallel()

	// Test that Colima is included in supported socket paths
	found := false
	for _, rt := range supportedSocketPaths {
		if rt == runtime.TypeColima {
			found = true
			break
		}
	}

	require.True(t, found, "TypeColima should be included in supportedSocketPaths")
}

func TestColimaConstants(t *testing.T) {
	t.Parallel()

	// Test that Colima constants are properly defined
	assert.Equal(t, "TOOLHIVE_COLIMA_SOCKET", ColimaSocketEnv)
	assert.Equal(t, ".colima/default/docker.sock", ColimaSocketPath)
	assert.Equal(t, runtime.Type("colima"), runtime.TypeColima)
}

func TestNewDockerClientWithColima(t *testing.T) {
	t.Parallel()

	// This test verifies that the NewDockerClient function can handle
	// the Colima runtime type in the supportedSocketPaths without errors
	// Note: This test won't actually connect since no container runtime is available
	// but it verifies the code paths don't panic and handle the new runtime type

	ctx := context.Background()
	_, _, _, err := NewDockerClient(ctx)

	// We expect an error since no container runtime is available in the test environment
	// but we're testing that the function doesn't panic or have compile errors
	// with the new Colima support
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no supported container runtime")
}
