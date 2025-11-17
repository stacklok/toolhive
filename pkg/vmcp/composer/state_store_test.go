package composer

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestInMemoryStateStore_SaveAndLoad tests basic save/load operations.
func TestInMemoryStateStore_SaveAndLoad(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	state := &WorkflowStatus{
		WorkflowID:     "test-workflow-1",
		Status:         WorkflowStatusRunning,
		CurrentStep:    "step1",
		CompletedSteps: []string{},
		StartTime:      time.Now(),
	}

	// Save state
	err := store.SaveState(ctx, state.WorkflowID, state)
	require.NoError(t, err)

	// Load state
	loaded, err := store.LoadState(ctx, state.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, state.WorkflowID, loaded.WorkflowID)
	assert.Equal(t, state.Status, loaded.Status)
	assert.Equal(t, state.CurrentStep, loaded.CurrentStep)
}

// TestInMemoryStateStore_LoadNotFound tests loading non-existent workflow.
func TestInMemoryStateStore_LoadNotFound(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	_, err := store.LoadState(ctx, "non-existent")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkflowNotFound)
}

// TestInMemoryStateStore_Delete tests workflow deletion.
func TestInMemoryStateStore_Delete(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	state := &WorkflowStatus{
		WorkflowID: "test-workflow-1",
		Status:     WorkflowStatusCompleted,
	}

	// Save and verify
	err := store.SaveState(ctx, state.WorkflowID, state)
	require.NoError(t, err)

	_, err = store.LoadState(ctx, state.WorkflowID)
	require.NoError(t, err)

	// Delete
	err = store.DeleteState(ctx, state.WorkflowID)
	require.NoError(t, err)

	// Verify deleted
	_, err = store.LoadState(ctx, state.WorkflowID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkflowNotFound)
}

// TestInMemoryStateStore_DeleteNotFound tests deleting non-existent workflow.
func TestInMemoryStateStore_DeleteNotFound(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	err := store.DeleteState(ctx, "non-existent")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrWorkflowNotFound)
}

// TestInMemoryStateStore_ListActiveWorkflows tests listing active workflows.
func TestInMemoryStateStore_ListActiveWorkflows(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	// Create workflows in various states
	workflows := []struct {
		id     string
		status WorkflowStatusType
		active bool
	}{
		{"wf1", WorkflowStatusRunning, true},
		{"wf2", WorkflowStatusWaitingForElicitation, true},
		{"wf3", WorkflowStatusPending, true},
		{"wf4", WorkflowStatusCompleted, false},
		{"wf5", WorkflowStatusFailed, false},
		{"wf6", WorkflowStatusCancelled, false},
		{"wf7", WorkflowStatusTimedOut, false},
	}

	for _, wf := range workflows {
		state := &WorkflowStatus{
			WorkflowID: wf.id,
			Status:     wf.status,
		}
		err := store.SaveState(ctx, wf.id, state)
		require.NoError(t, err)
	}

	// List active workflows
	activeIDs, err := store.ListActiveWorkflows(ctx)
	require.NoError(t, err)

	// Should only include running, waiting, and pending
	assert.Len(t, activeIDs, 3)

	// Verify the right ones are included
	activeMap := make(map[string]bool)
	for _, id := range activeIDs {
		activeMap[id] = true
	}

	for _, wf := range workflows {
		if wf.active {
			assert.True(t, activeMap[wf.id], "workflow %s should be in active list", wf.id)
		} else {
			assert.False(t, activeMap[wf.id], "workflow %s should not be in active list", wf.id)
		}
	}
}

// TestInMemoryStateStore_Cleanup tests automatic cleanup of stale workflows.
func TestInMemoryStateStore_Cleanup(t *testing.T) {
	t.Parallel()
	// Use very short intervals for testing but with sufficient margin
	cleanupInterval := 50 * time.Millisecond
	maxAge := 50 * time.Millisecond

	store := NewInMemoryStateStore(cleanupInterval, maxAge).(*inMemoryStateStore)
	defer store.Stop()

	// Create workflows directly in the store with specific timestamps
	veryOldTime := time.Now().Add(-1 * time.Second) // Way older than maxAge

	store.mu.Lock()
	// Old completed workflow - should be cleaned up
	store.states["old-workflow"] = &WorkflowStatus{
		WorkflowID:     "old-workflow",
		Status:         WorkflowStatusCompleted,
		LastUpdateTime: veryOldTime,
	}

	// Old running workflow - should NOT be cleaned up (still running)
	store.states["running-workflow"] = &WorkflowStatus{
		WorkflowID:     "running-workflow",
		Status:         WorkflowStatusRunning,
		LastUpdateTime: veryOldTime,
	}
	store.mu.Unlock()

	// Wait for at least 2 cleanup cycles
	time.Sleep(150 * time.Millisecond)

	// Verify cleanup results
	store.mu.RLock()
	oldExists := store.states["old-workflow"]
	runningExists := store.states["running-workflow"]
	store.mu.RUnlock()

	// Old completed workflow should be cleaned up
	assert.Nil(t, oldExists, "old completed workflow should be cleaned up")

	// Running workflow should still exist (not a terminal state)
	assert.NotNil(t, runningExists, "running workflow should not be cleaned up")
}

// TestInMemoryStateStore_GetStats tests statistics retrieval.
func TestInMemoryStateStore_GetStats(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour).(*inMemoryStateStore)
	ctx := context.Background()

	// Create workflows in various states
	states := []WorkflowStatusType{
		WorkflowStatusPending,
		WorkflowStatusRunning,
		WorkflowStatusRunning,
		WorkflowStatusWaitingForElicitation,
		WorkflowStatusCompleted,
		WorkflowStatusCompleted,
		WorkflowStatusCompleted,
		WorkflowStatusFailed,
		WorkflowStatusCancelled,
		WorkflowStatusTimedOut,
	}

	for i, status := range states {
		state := &WorkflowStatus{
			WorkflowID: string(rune('a' + i)),
			Status:     status,
		}
		err := store.SaveState(ctx, state.WorkflowID, state)
		require.NoError(t, err)
	}

	stats := store.GetStats()

	assert.Equal(t, len(states), stats["total"])
	assert.Equal(t, 1, stats["pending"])
	assert.Equal(t, 2, stats["running"])
	assert.Equal(t, 1, stats["waiting_for_elicit"])
	assert.Equal(t, 3, stats["completed"])
	assert.Equal(t, 1, stats["failed"])
	assert.Equal(t, 1, stats["cancelled"])
	assert.Equal(t, 1, stats["timed_out"])
}

// TestInMemoryStateStore_Concurrency tests concurrent access to state store.
func TestInMemoryStateStore_Concurrency(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	// Run multiple goroutines concurrently
	const numGoroutines = 50
	const opsPerGoroutine = 100

	done := make(chan bool, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < opsPerGoroutine; j++ {
				workflowID := string(rune('a' + (id % 26)))

				state := &WorkflowStatus{
					WorkflowID: workflowID,
					Status:     WorkflowStatusRunning,
				}

				// Save
				_ = store.SaveState(ctx, workflowID, state)

				// Load
				_, _ = store.LoadState(ctx, workflowID)

				// List
				_, _ = store.ListActiveWorkflows(ctx)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines to complete
	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	// Verify store is still functional
	state := &WorkflowStatus{
		WorkflowID: "final-test",
		Status:     WorkflowStatusCompleted,
	}

	err := store.SaveState(ctx, state.WorkflowID, state)
	require.NoError(t, err)

	loaded, err := store.LoadState(ctx, state.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, state.WorkflowID, loaded.WorkflowID)
}

// TestInMemoryStateStore_DeepCopy tests that state is deep copied to prevent external modifications.
func TestInMemoryStateStore_DeepCopy(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	state := &WorkflowStatus{
		WorkflowID:     "test-workflow",
		Status:         WorkflowStatusRunning,
		CompletedSteps: []string{"step1", "step2"},
		PendingElicitations: []*PendingElicitation{
			{StepID: "elicit1", Message: "test"},
		},
	}

	// Save state
	err := store.SaveState(ctx, state.WorkflowID, state)
	require.NoError(t, err)

	// Modify original state
	state.Status = WorkflowStatusFailed
	state.CompletedSteps[0] = "modified"
	state.PendingElicitations[0].Message = "modified"

	// Load state and verify it wasn't modified
	loaded, err := store.LoadState(ctx, state.WorkflowID)
	require.NoError(t, err)

	assert.Equal(t, WorkflowStatusRunning, loaded.Status, "status should not be modified")
	assert.Equal(t, "step1", loaded.CompletedSteps[0], "completed steps should not be modified")
	assert.Equal(t, "test", loaded.PendingElicitations[0].Message, "pending elicitations should not be modified")

	// Modify loaded state
	loaded.CompletedSteps[0] = "another-modification"

	// Load again and verify internal state wasn't modified
	loaded2, err := store.LoadState(ctx, state.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, "step1", loaded2.CompletedSteps[0], "internal state should not be modified")
}

// TestInMemoryStateStore_UpdateExisting tests updating existing workflow state.
func TestInMemoryStateStore_UpdateExisting(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	// Create initial state
	state := &WorkflowStatus{
		WorkflowID:     "test-workflow",
		Status:         WorkflowStatusRunning,
		CompletedSteps: []string{"step1"},
	}

	err := store.SaveState(ctx, state.WorkflowID, state)
	require.NoError(t, err)

	// Update state
	state.Status = WorkflowStatusCompleted
	state.CompletedSteps = append(state.CompletedSteps, "step2", "step3")

	err = store.SaveState(ctx, state.WorkflowID, state)
	require.NoError(t, err)

	// Load and verify
	loaded, err := store.LoadState(ctx, state.WorkflowID)
	require.NoError(t, err)

	assert.Equal(t, WorkflowStatusCompleted, loaded.Status)
	assert.Equal(t, []string{"step1", "step2", "step3"}, loaded.CompletedSteps)
}

// TestInMemoryStateStore_ValidationErrors tests validation of inputs.
func TestInMemoryStateStore_ValidationErrors(t *testing.T) {
	t.Parallel()
	store := NewInMemoryStateStore(1*time.Minute, 1*time.Hour)
	ctx := context.Background()

	// Test empty workflow ID
	err := store.SaveState(ctx, "", &WorkflowStatus{})
	assert.Error(t, err)

	// Test nil state
	err = store.SaveState(ctx, "test", nil)
	assert.Error(t, err)

	// Test load with empty ID
	_, err = store.LoadState(ctx, "")
	assert.Error(t, err)

	// Test delete with empty ID
	err = store.DeleteState(ctx, "")
	assert.Error(t, err)
}
