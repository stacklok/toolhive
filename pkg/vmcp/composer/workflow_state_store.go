// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"context"
	"fmt"
	"sync"
)

// InMemoryWorkflowStateStore implements WorkflowStateStore with in-memory storage.
//
// This implementation stores workflow state in memory, which means:
//   - State is lost on server restart
//   - No support for distributed/multi-instance deployments
//   - Fast access with no I/O overhead
//
// This is suitable for Phase 2 (basic elicitation support). Future phases
// can implement Redis/DB backends for persistence and distribution.
//
// Thread-safety: Safe for concurrent access using sync.RWMutex.
type InMemoryWorkflowStateStore struct {
	// workflows stores workflow state by workflow ID
	workflows map[string]*WorkflowStatus
	mu        sync.RWMutex
}

// NewInMemoryWorkflowStateStore creates a new in-memory workflow state store.
func NewInMemoryWorkflowStateStore() WorkflowStateStore {
	return &InMemoryWorkflowStateStore{
		workflows: make(map[string]*WorkflowStatus),
	}
}

// SaveState persists workflow state.
//
// If a workflow with the same ID already exists, it is overwritten.
// This is thread-safe for concurrent saves.
func (s *InMemoryWorkflowStateStore) SaveState(
	_ context.Context,
	workflowID string,
	state *WorkflowStatus,
) error {
	if workflowID == "" {
		return fmt.Errorf("workflow ID cannot be empty")
	}
	if state == nil {
		return fmt.Errorf("workflow state cannot be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clone the state to prevent external modifications
	clonedState := cloneWorkflowStatus(state)
	s.workflows[workflowID] = clonedState

	return nil
}

// LoadState retrieves workflow state.
//
// Returns ErrWorkflowNotFound if the workflow does not exist.
// The returned state is a clone to prevent external modifications.
func (s *InMemoryWorkflowStateStore) LoadState(
	_ context.Context,
	workflowID string,
) (*WorkflowStatus, error) {
	if workflowID == "" {
		return nil, fmt.Errorf("workflow ID cannot be empty")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	state, exists := s.workflows[workflowID]
	if !exists {
		return nil, fmt.Errorf("%w: %s", ErrWorkflowNotFound, workflowID)
	}

	// Clone the state to prevent external modifications
	return cloneWorkflowStatus(state), nil
}

// DeleteState removes workflow state.
//
// This is idempotent - deleting a non-existent workflow is not an error.
func (s *InMemoryWorkflowStateStore) DeleteState(
	_ context.Context,
	workflowID string,
) error {
	if workflowID == "" {
		return fmt.Errorf("workflow ID cannot be empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.workflows, workflowID)
	return nil
}

// ListActiveWorkflows returns all active workflow IDs.
//
// A workflow is considered active if it has state stored in the store.
// The returned list is a snapshot at the time of the call.
func (s *InMemoryWorkflowStateStore) ListActiveWorkflows(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := make([]string, 0, len(s.workflows))
	for id := range s.workflows {
		ids = append(ids, id)
	}

	return ids, nil
}

// cloneWorkflowStatus creates a deep copy of WorkflowStatus.
// This prevents external modifications to stored state.
func cloneWorkflowStatus(state *WorkflowStatus) *WorkflowStatus {
	if state == nil {
		return nil
	}

	clone := &WorkflowStatus{
		WorkflowID:     state.WorkflowID,
		Status:         state.Status,
		CurrentStep:    state.CurrentStep,
		CompletedSteps: make([]string, len(state.CompletedSteps)),
		StartTime:      state.StartTime,
		LastUpdateTime: state.LastUpdateTime,
	}

	// Clone completed steps
	copy(clone.CompletedSteps, state.CompletedSteps)

	// Clone pending elicitations
	if len(state.PendingElicitations) > 0 {
		clone.PendingElicitations = make([]*PendingElicitation, len(state.PendingElicitations))
		for i, pe := range state.PendingElicitations {
			clone.PendingElicitations[i] = clonePendingElicitation(pe)
		}
	}

	return clone
}

// clonePendingElicitation creates a deep copy of PendingElicitation.
func clonePendingElicitation(pe *PendingElicitation) *PendingElicitation {
	if pe == nil {
		return nil
	}

	return &PendingElicitation{
		StepID:    pe.StepID,
		Message:   pe.Message,
		Schema:    cloneMap(pe.Schema),
		ExpiresAt: pe.ExpiresAt,
	}
}
