package composer

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testModifiedWorkflowID = "modified"

func TestInMemoryWorkflowStateStore_SaveState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		workflowID  string
		state       *WorkflowStatus
		wantErr     bool
		errContains string
	}{
		{
			name:       "success",
			workflowID: "workflow-1",
			state: &WorkflowStatus{
				WorkflowID:     "workflow-1",
				Status:         WorkflowStatusRunning,
				CurrentStep:    "step-1",
				CompletedSteps: []string{"step-0"},
				StartTime:      time.Now(),
			},
			wantErr: false,
		},
		{
			name:        "empty_workflow_id",
			workflowID:  "",
			state:       &WorkflowStatus{},
			wantErr:     true,
			errContains: "workflow ID cannot be empty",
		},
		{
			name:        "nil_state",
			workflowID:  "workflow-1",
			state:       nil,
			wantErr:     true,
			errContains: "workflow state cannot be nil",
		},
		{
			name:       "overwrite_existing",
			workflowID: "workflow-1",
			state: &WorkflowStatus{
				WorkflowID: "workflow-1",
				Status:     WorkflowStatusCompleted,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := NewInMemoryWorkflowStateStore()
			ctx := context.Background()

			err := store.SaveState(ctx, tt.workflowID, tt.state)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestInMemoryWorkflowStateStore_LoadState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(WorkflowStateStore) error
		workflowID  string
		wantErr     bool
		errType     error
		errContains string
	}{
		{
			name: "success",
			setup: func(store WorkflowStateStore) error {
				return store.SaveState(context.Background(), "workflow-1", &WorkflowStatus{
					WorkflowID:     "workflow-1",
					Status:         WorkflowStatusRunning,
					CompletedSteps: []string{"step-1"},
				})
			},
			workflowID: "workflow-1",
			wantErr:    false,
		},
		{
			name:       "not_found",
			setup:      func(_ WorkflowStateStore) error { return nil },
			workflowID: "nonexistent",
			wantErr:    true,
			errType:    ErrWorkflowNotFound,
		},
		{
			name:        "empty_workflow_id",
			setup:       func(_ WorkflowStateStore) error { return nil },
			workflowID:  "",
			wantErr:     true,
			errContains: "workflow ID cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := NewInMemoryWorkflowStateStore()
			ctx := context.Background()

			err := tt.setup(store)
			require.NoError(t, err)

			state, err := store.LoadState(ctx, tt.workflowID)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errType != nil {
					assert.ErrorIs(t, err, tt.errType)
				}
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				require.NoError(t, err)
				require.NotNil(t, state)
				assert.Equal(t, tt.workflowID, state.WorkflowID)
			}
		})
	}
}

func TestInMemoryWorkflowStateStore_DeleteState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(WorkflowStateStore) error
		workflowID  string
		wantErr     bool
		errContains string
	}{
		{
			name: "success",
			setup: func(store WorkflowStateStore) error {
				return store.SaveState(context.Background(), "workflow-1", &WorkflowStatus{
					WorkflowID: "workflow-1",
				})
			},
			workflowID: "workflow-1",
			wantErr:    false,
		},
		{
			name:       "idempotent_nonexistent",
			setup:      func(_ WorkflowStateStore) error { return nil },
			workflowID: "nonexistent",
			wantErr:    false,
		},
		{
			name:        "empty_workflow_id",
			setup:       func(_ WorkflowStateStore) error { return nil },
			workflowID:  "",
			wantErr:     true,
			errContains: "workflow ID cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := NewInMemoryWorkflowStateStore()
			ctx := context.Background()

			err := tt.setup(store)
			require.NoError(t, err)

			err = store.DeleteState(ctx, tt.workflowID)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
			} else {
				require.NoError(t, err)
				// Verify it was deleted
				if tt.workflowID != "" {
					_, err := store.LoadState(ctx, tt.workflowID)
					assert.ErrorIs(t, err, ErrWorkflowNotFound)
				}
			}
		})
	}
}

func TestInMemoryWorkflowStateStore_ListActiveWorkflows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		setup   func(WorkflowStateStore) error
		wantIDs []string
		wantErr bool
	}{
		{
			name: "multiple_workflows",
			setup: func(store WorkflowStateStore) error {
				ctx := context.Background()
				if err := store.SaveState(ctx, "workflow-1", &WorkflowStatus{WorkflowID: "workflow-1"}); err != nil {
					return err
				}
				if err := store.SaveState(ctx, "workflow-2", &WorkflowStatus{WorkflowID: "workflow-2"}); err != nil {
					return err
				}
				return store.SaveState(ctx, "workflow-3", &WorkflowStatus{WorkflowID: "workflow-3"})
			},
			wantIDs: []string{"workflow-1", "workflow-2", "workflow-3"},
			wantErr: false,
		},
		{
			name:    "empty_store",
			setup:   func(_ WorkflowStateStore) error { return nil },
			wantIDs: []string{},
			wantErr: false,
		},
		{
			name: "after_deletion",
			setup: func(store WorkflowStateStore) error {
				ctx := context.Background()
				if err := store.SaveState(ctx, "workflow-1", &WorkflowStatus{WorkflowID: "workflow-1"}); err != nil {
					return err
				}
				if err := store.SaveState(ctx, "workflow-2", &WorkflowStatus{WorkflowID: "workflow-2"}); err != nil {
					return err
				}
				return store.DeleteState(ctx, "workflow-1")
			},
			wantIDs: []string{"workflow-2"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := NewInMemoryWorkflowStateStore()
			ctx := context.Background()

			err := tt.setup(store)
			require.NoError(t, err)

			ids, err := store.ListActiveWorkflows(ctx)

			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.ElementsMatch(t, tt.wantIDs, ids)
			}
		})
	}
}

func TestInMemoryWorkflowStateStore_StateIsolation(t *testing.T) {
	t.Parallel()

	store := NewInMemoryWorkflowStateStore()
	ctx := context.Background()

	// Create original state
	original := &WorkflowStatus{
		WorkflowID:     "workflow-1",
		Status:         WorkflowStatusRunning,
		CompletedSteps: []string{"step-1"},
		PendingElicitations: []*PendingElicitation{
			{
				StepID:  "elicit-1",
				Message: "Test?",
				Schema:  map[string]any{"type": "object"},
			},
		},
	}

	err := store.SaveState(ctx, "workflow-1", original)
	require.NoError(t, err)

	// Load state
	loaded, err := store.LoadState(ctx, "workflow-1")
	require.NoError(t, err)

	// Modify loaded state
	loaded.Status = WorkflowStatusCompleted
	loaded.CompletedSteps = append(loaded.CompletedSteps, "step-2")
	loaded.PendingElicitations[0].Message = "Modified"

	// Load again - should not be affected by modifications
	loaded2, err := store.LoadState(ctx, "workflow-1")
	require.NoError(t, err)

	assert.Equal(t, WorkflowStatusRunning, loaded2.Status)
	assert.Equal(t, []string{"step-1"}, loaded2.CompletedSteps)
	assert.Equal(t, "Test?", loaded2.PendingElicitations[0].Message)
}

func TestInMemoryWorkflowStateStore_Concurrent(t *testing.T) {
	t.Parallel()

	store := NewInMemoryWorkflowStateStore()
	ctx := context.Background()

	const numGoroutines = 50
	var wg sync.WaitGroup
	wg.Add(numGoroutines * 3) // Save, Load, Delete operations

	// Concurrent saves
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			workflowID := "workflow-" + string(rune('0'+id%10))
			state := &WorkflowStatus{
				WorkflowID: workflowID,
				Status:     WorkflowStatusRunning,
			}
			_ = store.SaveState(ctx, workflowID, state)
		}(i)
	}

	// Concurrent loads
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			workflowID := "workflow-" + string(rune('0'+id%10))
			_, _ = store.LoadState(ctx, workflowID)
		}(i)
	}

	// Concurrent deletes
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			workflowID := "workflow-" + string(rune('0'+id%10))
			_ = store.DeleteState(ctx, workflowID)
		}(i)
	}

	wg.Wait()

	// Should not panic and store should be in consistent state
	ids, err := store.ListActiveWorkflows(ctx)
	require.NoError(t, err)
	// Number of active workflows is non-deterministic due to concurrency
	assert.LessOrEqual(t, len(ids), 10)
}

func TestCloneWorkflowStatus(t *testing.T) {
	t.Parallel()

	original := &WorkflowStatus{
		WorkflowID:     "workflow-1",
		Status:         WorkflowStatusWaitingForElicitation,
		CurrentStep:    "step-2",
		CompletedSteps: []string{"step-1"},
		PendingElicitations: []*PendingElicitation{
			{
				StepID:    "elicit-1",
				Message:   "Confirm?",
				Schema:    map[string]any{"type": "object", "prop": "value"},
				ExpiresAt: time.Now().Add(5 * time.Minute),
			},
		},
		StartTime:      time.Now().Add(-1 * time.Hour),
		LastUpdateTime: time.Now(),
	}

	clone := cloneWorkflowStatus(original)

	// Verify deep copy
	require.NotNil(t, clone)
	assert.Equal(t, original.WorkflowID, clone.WorkflowID)
	assert.Equal(t, original.Status, clone.Status)
	assert.Equal(t, original.CurrentStep, clone.CurrentStep)
	assert.Equal(t, original.CompletedSteps, clone.CompletedSteps)
	assert.Equal(t, len(original.PendingElicitations), len(clone.PendingElicitations))

	// Verify independence (modifications don't affect original)
	clone.WorkflowID = testModifiedWorkflowID
	clone.CompletedSteps = append(clone.CompletedSteps, "step-2")
	clone.PendingElicitations[0].Message = testModifiedWorkflowID
	clone.PendingElicitations[0].Schema["new"] = "value"

	assert.NotEqual(t, original.WorkflowID, clone.WorkflowID)
	assert.Equal(t, 1, len(original.CompletedSteps))
	assert.Equal(t, "Confirm?", original.PendingElicitations[0].Message)
	assert.NotContains(t, original.PendingElicitations[0].Schema, "new")
}

func TestCloneWorkflowStatus_NilHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input *WorkflowStatus
	}{
		{
			name:  "nil_input",
			input: nil,
		},
		{
			name: "nil_slices",
			input: &WorkflowStatus{
				WorkflowID:          "workflow-1",
				CompletedSteps:      nil,
				PendingElicitations: nil,
			},
		},
		{
			name: "empty_slices",
			input: &WorkflowStatus{
				WorkflowID:          "workflow-1",
				CompletedSteps:      []string{},
				PendingElicitations: []*PendingElicitation{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := cloneWorkflowStatus(tt.input)

			// For nil input, result should be nil
			if tt.input == nil {
				assert.Nil(t, result)
				return
			}

			// For non-nil input, verify fields match
			require.NotNil(t, result)
			assert.Equal(t, tt.input.WorkflowID, result.WorkflowID)
			assert.Equal(t, tt.input.Status, result.Status)
			assert.Equal(t, tt.input.CurrentStep, result.CurrentStep)

			// For slices, verify lengths match (nil and empty are both valid)
			assert.Len(t, result.CompletedSteps, len(tt.input.CompletedSteps))
			assert.Len(t, result.PendingElicitations, len(tt.input.PendingElicitations))
		})
	}
}
