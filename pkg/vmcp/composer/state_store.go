// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
)

// inMemoryStateStore implements WorkflowStateStore using in-memory storage.
// This is suitable for single-instance deployments and testing.
// For production multi-instance deployments, use a distributed store (Redis, DB, etc.).
type inMemoryStateStore struct {
	mu     sync.RWMutex
	states map[string]*WorkflowStatus

	// cleanupInterval defines how often to run cleanup of stale workflows.
	cleanupInterval time.Duration

	// maxAge defines how long to keep completed/failed workflows.
	maxAge time.Duration

	// stopCleanup signals the cleanup goroutine to stop.
	stopCleanup chan struct{}

	// cleanupDone signals when cleanup goroutine has stopped.
	cleanupDone chan struct{}
}

// NewInMemoryStateStore creates a new in-memory workflow state store.
// Cleanup runs periodically to remove stale workflows.
func NewInMemoryStateStore(cleanupInterval, maxAge time.Duration) WorkflowStateStore {
	if cleanupInterval <= 0 {
		cleanupInterval = 5 * time.Minute
	}
	if maxAge <= 0 {
		maxAge = 1 * time.Hour
	}

	store := &inMemoryStateStore{
		states:          make(map[string]*WorkflowStatus),
		cleanupInterval: cleanupInterval,
		maxAge:          maxAge,
		stopCleanup:     make(chan struct{}),
		cleanupDone:     make(chan struct{}),
	}

	// Start cleanup goroutine
	go store.runCleanup()

	return store
}

// SaveState persists workflow state to memory.
func (s *inMemoryStateStore) SaveState(_ context.Context, workflowID string, state *WorkflowStatus) error {
	if workflowID == "" {
		return fmt.Errorf("workflow ID is required")
	}
	if state == nil {
		return fmt.Errorf("state is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Update last update time
	state.LastUpdateTime = time.Now()

	// Deep copy to prevent external modifications.
	// Note: We perform a shallow copy of the WorkflowStatus struct and deep copy slices
	// (CompletedSteps, PendingElicitations). Maps within nested structures (like
	// PendingElicitation.Schema) remain shared. This is acceptable because:
	// 1. WorkflowStatus is used for state tracking, not as a data manipulation structure
	// 2. The state store is append-only for completed steps during workflow execution
	// 3. Full deep copying of arbitrary nested maps would be expensive and unnecessary
	stateCopy := *state
	stateCopy.CompletedSteps = make([]string, len(state.CompletedSteps))
	copy(stateCopy.CompletedSteps, state.CompletedSteps)

	if len(state.PendingElicitations) > 0 {
		stateCopy.PendingElicitations = make([]*PendingElicitation, len(state.PendingElicitations))
		for i, pe := range state.PendingElicitations {
			peCopy := *pe
			stateCopy.PendingElicitations[i] = &peCopy
		}
	}

	s.states[workflowID] = &stateCopy

	logger.Debugf("Saved state for workflow %s (status: %s)", workflowID, state.Status)
	return nil
}

// LoadState retrieves workflow state from memory.
func (s *inMemoryStateStore) LoadState(_ context.Context, workflowID string) (*WorkflowStatus, error) {
	if workflowID == "" {
		return nil, fmt.Errorf("workflow ID is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	state, exists := s.states[workflowID]
	if !exists {
		return nil, fmt.Errorf("%w: workflow %s", ErrWorkflowNotFound, workflowID)
	}

	// Deep copy to prevent external modifications
	stateCopy := *state
	stateCopy.CompletedSteps = make([]string, len(state.CompletedSteps))
	copy(stateCopy.CompletedSteps, state.CompletedSteps)

	if len(state.PendingElicitations) > 0 {
		stateCopy.PendingElicitations = make([]*PendingElicitation, len(state.PendingElicitations))
		for i, pe := range state.PendingElicitations {
			peCopy := *pe
			stateCopy.PendingElicitations[i] = &peCopy
		}
	}

	return &stateCopy, nil
}

// DeleteState removes workflow state from memory.
func (s *inMemoryStateStore) DeleteState(_ context.Context, workflowID string) error {
	if workflowID == "" {
		return fmt.Errorf("workflow ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.states[workflowID]; !exists {
		return fmt.Errorf("%w: workflow %s", ErrWorkflowNotFound, workflowID)
	}

	delete(s.states, workflowID)
	logger.Debugf("Deleted state for workflow %s", workflowID)
	return nil
}

// ListActiveWorkflows returns all active workflow IDs.
func (s *inMemoryStateStore) ListActiveWorkflows(_ context.Context) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var activeIDs []string
	for workflowID, state := range s.states {
		// Only include running or waiting workflows
		if state.Status == WorkflowStatusRunning ||
			state.Status == WorkflowStatusWaitingForElicitation ||
			state.Status == WorkflowStatusPending {
			activeIDs = append(activeIDs, workflowID)
		}
	}

	return activeIDs, nil
}

// Stop stops the cleanup goroutine and waits for it to finish.
func (s *inMemoryStateStore) Stop() {
	close(s.stopCleanup)
	<-s.cleanupDone
}

// runCleanup periodically removes stale workflows from the store.
func (s *inMemoryStateStore) runCleanup() {
	defer close(s.cleanupDone)

	ticker := time.NewTicker(s.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.cleanup()
		case <-s.stopCleanup:
			logger.Debugf("State store cleanup goroutine stopped")
			return
		}
	}
}

// cleanup removes workflows that have been completed/failed for too long.
func (s *inMemoryStateStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	removed := 0

	for workflowID, state := range s.states {
		// Check if workflow is in a terminal state
		isTerminal := state.Status == WorkflowStatusCompleted ||
			state.Status == WorkflowStatusFailed ||
			state.Status == WorkflowStatusCancelled ||
			state.Status == WorkflowStatusTimedOut

		// Remove if terminal and older than maxAge
		if isTerminal && now.Sub(state.LastUpdateTime) > s.maxAge {
			delete(s.states, workflowID)
			removed++
		}
	}

	if removed > 0 {
		logger.Debugf("Cleaned up %d stale workflow(s)", removed)
	}

	// Log state store metrics for observability (every cleanup cycle)
	s.logMetrics()
}

// logMetrics logs state store statistics for observability.
// Must be called with s.mu held.
func (s *inMemoryStateStore) logMetrics() {
	total := len(s.states)
	if total == 0 {
		return // Don't log if empty
	}

	// Count by status
	var running, pending, waiting, completed, failed, cancelled, timedOut int
	for _, state := range s.states {
		switch state.Status {
		case WorkflowStatusRunning:
			running++
		case WorkflowStatusPending:
			pending++
		case WorkflowStatusWaitingForElicitation:
			waiting++
		case WorkflowStatusCompleted:
			completed++
		case WorkflowStatusFailed:
			failed++
		case WorkflowStatusCancelled:
			cancelled++
		case WorkflowStatusTimedOut:
			timedOut++
		}
	}

	logger.Infof("Workflow state store metrics: total=%d, running=%d, pending=%d, waiting=%d, "+
		"completed=%d, failed=%d, cancelled=%d, timed_out=%d",
		total, running, pending, waiting, completed, failed, cancelled, timedOut)
}

// GetStats returns statistics about the state store.
func (s *inMemoryStateStore) GetStats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := map[string]int{
		"total":              0,
		"pending":            0,
		"running":            0,
		"waiting_for_elicit": 0,
		"completed":          0,
		"failed":             0,
		"cancelled":          0,
		"timed_out":          0,
	}

	for _, state := range s.states {
		stats["total"]++
		switch state.Status {
		case WorkflowStatusPending:
			stats["pending"]++
		case WorkflowStatusRunning:
			stats["running"]++
		case WorkflowStatusWaitingForElicitation:
			stats["waiting_for_elicit"]++
		case WorkflowStatusCompleted:
			stats["completed"]++
		case WorkflowStatusFailed:
			stats["failed"]++
		case WorkflowStatusCancelled:
			stats["cancelled"]++
		case WorkflowStatusTimedOut:
			stats["timed_out"]++
		}
	}

	return stats
}
