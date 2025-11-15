package workloads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	configMocks "github.com/stacklok/toolhive/pkg/config/mocks"
	runtimeMocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
)

func TestNewManagerFromRuntime(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := runtimeMocks.NewMockRuntime(ctrl)

	// The NewManagerFromRuntime will try to create a status manager, which requires runtime methods
	// For this test, we can just verify the structure is created correctly
	manager, err := NewManagerFromRuntime(mockRuntime)

	require.NoError(t, err)
	require.NotNil(t, manager)

	// Verify it's a cliManager with the runtime set
	cliMgr, ok := manager.(*cliManager)
	require.True(t, ok)
	assert.Equal(t, mockRuntime, cliMgr.runtime)
	assert.NotNil(t, cliMgr.statuses)
	assert.NotNil(t, cliMgr.configProvider)
}

func TestNewManagerFromRuntimeWithProvider(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRuntime := runtimeMocks.NewMockRuntime(ctrl)
	mockConfigProvider := configMocks.NewMockProvider(ctrl)

	manager, err := NewManagerFromRuntimeWithProvider(mockRuntime, mockConfigProvider)

	require.NoError(t, err)
	require.NotNil(t, manager)

	cliMgr, ok := manager.(*cliManager)
	require.True(t, ok)
	assert.Equal(t, mockRuntime, cliMgr.runtime)
	assert.Equal(t, mockConfigProvider, cliMgr.configProvider)
	assert.NotNil(t, cliMgr.statuses)
}
