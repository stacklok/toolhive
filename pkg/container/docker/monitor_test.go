package docker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	rtmocks "github.com/stacklok/toolhive/pkg/container/runtime/mocks"
)

func TestNewMonitor_Constructs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	m := NewMonitor(mockRT, "workload-1")
	require.NotNil(t, m)
}

func TestContainerMonitor_StartMonitoring_WhenRunningStarts(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartMonitoring should verify running exactly once on first call.
	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-1").Return(true, nil).Times(1)

	m := NewMonitor(mockRT, "workload-1")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)
	require.NotNil(t, ch)

	// Idempotent: subsequent call returns same channel and does not call runtime again.
	ch2, err := m.StartMonitoring(ctx)
	require.NoError(t, err)
	assert.Equal(t, ch, ch2)

	// Ensure StopMonitoring is safe and unblocks the background goroutine quickly
	// without needing to wait for the 5s ticker.
	m.StopMonitoring()
}

func TestContainerMonitor_StartMonitoring_WhenNotRunningErrors(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx := context.Background()

	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-2").Return(false, nil)

	m := NewMonitor(mockRT, "workload-2")
	ch, err := m.StartMonitoring(ctx)
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.ErrorIs(t, err, ErrContainerNotRunning)
}

func TestContainerMonitor_StartMonitoring_RuntimeErrorBubblesUp(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	ctx := context.Background()

	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-3").Return(false, errors.New("boom"))

	m := NewMonitor(mockRT, "workload-3")
	ch, err := m.StartMonitoring(ctx)
	require.Error(t, err)
	assert.Nil(t, ch)
	assert.EqualError(t, err, "boom")
}

func TestContainerMonitor_StopMonitoring_NotRunningIsNoop(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	// Construct monitor but do not start
	m := NewMonitor(mockRT, "workload-4")
	// Should not panic or deadlock
	m.StopMonitoring()
}

func TestIsContainerNotFound(t *testing.T) {
	t.Parallel()

	t.Run("direct", func(t *testing.T) {
		t.Parallel()
		assert.True(t, IsContainerNotFound(ErrContainerNotFound))
	})

	t.Run("wrapped", func(t *testing.T) {
		t.Parallel()
		err := NewContainerError(ErrContainerNotFound, "cid", "not found")
		assert.True(t, IsContainerNotFound(err))
	})

	t.Run("other", func(t *testing.T) {
		t.Parallel()
		assert.False(t, IsContainerNotFound(errors.New("different")))
	})

	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		assert.False(t, IsContainerNotFound(nil))
	})
}

func TestContainerMonitor_StartStop_TerminatesQuickly(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockRT := rtmocks.NewMockRuntime(ctrl)

	// Start path: initially running
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mockRT.EXPECT().IsWorkloadRunning(ctx, "workload-5").Return(true, nil).Times(1)

	m := NewMonitor(mockRT, "workload-5")
	ch, err := m.StartMonitoring(ctx)
	require.NoError(t, err)
	require.NotNil(t, ch)

	// Stop should complete promptly (no waits on the 5s ticker).
	done := make(chan struct{})
	go func() {
		m.StopMonitoring()
		close(done)
	}()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Fatal("StopMonitoring did not return promptly")
	}
}

// The following stubs are required by the Runtime interface when using gomock in these tests,
// but they are not exercised by these specific scenarios. We let gomock enforce that they are
// not called by not setting any expectations for them.
// ListWorkloads, RemoveWorkload, GetWorkloadLogs, GetWorkloadInfo, IsRunning are already available
// on the generated mock. We include a minimal compile-time assertion on Monitor usage here.
var _ rt.Monitor = (*ContainerMonitor)(nil)
