// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/stacklok/toolhive/pkg/logger"
)

const (
	// defaultMaxParallelSteps is the default maximum number of steps to execute in parallel.
	defaultMaxParallelSteps = 10
	failureModeContinue     = "continue"
)

// dagExecutor executes workflow steps using a Directed Acyclic Graph (DAG) approach.
// It supports parallel execution of independent steps while respecting dependencies.
type dagExecutor struct {
	// maxParallel limits the number of steps executing concurrently.
	maxParallel int

	// semaphore controls concurrent execution.
	semaphore chan struct{}
}

// newDAGExecutor creates a new DAG executor with the specified maximum parallelism.
func newDAGExecutor(maxParallel int) *dagExecutor {
	if maxParallel <= 0 {
		maxParallel = defaultMaxParallelSteps
	}

	return &dagExecutor{
		maxParallel: maxParallel,
		semaphore:   make(chan struct{}, maxParallel),
	}
}

// executionLevel represents a group of steps that can be executed in parallel.
type executionLevel struct {
	steps []*WorkflowStep
}

// executeDAG executes workflow steps using DAG-based parallel execution.
//
// The algorithm works as follows:
//  1. Build a dependency graph from the steps
//  2. Perform topological sort to identify execution levels
//  3. Execute each level in parallel (steps within a level are independent)
//  4. Wait for all steps in a level to complete before proceeding to next level
//  5. Aggregate errors and handle based on failure mode
func (d *dagExecutor) executeDAG(
	ctx context.Context,
	steps []WorkflowStep,
	execFunc func(context.Context, *WorkflowStep) error,
	failureMode string,
) error {
	if len(steps) == 0 {
		return nil
	}

	// Build execution levels using topological sort
	levels, err := d.buildExecutionLevels(steps)
	if err != nil {
		return fmt.Errorf("failed to build execution levels: %w", err)
	}

	// Log execution plan statistics for observability
	stats := d.getExecutionStats(levels)
	logger.Infof("Workflow execution plan: %d levels, %d total steps, max parallelism: %d",
		stats["total_levels"], stats["total_steps"], stats["max_parallelism"])

	// Execute each level
	for levelIdx, level := range levels {
		logger.Debugf("Executing level %d with %d steps", levelIdx, len(level.steps))

		// Execute all steps in this level in parallel
		if err := d.executeLevel(ctx, level, execFunc, failureMode); err != nil {
			return err
		}
	}

	return nil
}

// executeLevel executes all steps in a level in parallel.
func (d *dagExecutor) executeLevel(
	ctx context.Context,
	level *executionLevel,
	execFunc func(context.Context, *WorkflowStep) error,
	failureMode string,
) error {
	// Use errgroup for coordinated parallel execution
	g, groupCtx := errgroup.WithContext(ctx)

	// Track errors from steps that should continue
	var errorsMu sync.Mutex
	var continuedErrors []error

	// Execute each step in the level
	for _, step := range level.steps {
		step := step // Capture loop variable

		g.Go(func() error {
			// Acquire semaphore
			select {
			case d.semaphore <- struct{}{}:
				defer func() { <-d.semaphore }()
			case <-groupCtx.Done():
				return groupCtx.Err()
			}

			// Execute the step
			err := execFunc(groupCtx, step)
			if err != nil {
				logger.Errorf("Step %s failed: %v", step.ID, err)

				// Check if we should continue despite the error
				shouldContinue := d.shouldContinueOnError(step, failureMode)
				if shouldContinue {
					errorsMu.Lock()
					continuedErrors = append(continuedErrors, err)
					errorsMu.Unlock()
					return nil // Don't fail the errgroup
				}

				return err
			}

			logger.Debugf("Step %s completed successfully", step.ID)
			return nil
		})
	}

	// Wait for all steps in the level to complete
	if err := g.Wait(); err != nil {
		return fmt.Errorf("level execution failed: %w", err)
	}

	// Log continued errors if any
	if len(continuedErrors) > 0 {
		logger.Warnf("Level completed with %d continued errors (mode: %s)", len(continuedErrors), failureMode)
	}

	return nil
}

// shouldContinueOnError determines if execution should continue after a step error.
func (*dagExecutor) shouldContinueOnError(step *WorkflowStep, failureMode string) bool {
	// Check step-level error handling
	if step.OnError != nil && step.OnError.ContinueOnError {
		return true
	}

	// Check workflow-level failure mode
	return failureMode == failureModeContinue
}

// buildExecutionLevels performs topological sort to build execution levels.
//
// Returns a slice of execution levels, where each level contains steps that:
//  1. Have no unmet dependencies (all dependencies are in previous levels)
//  2. Can be executed in parallel with other steps in the same level
func (*dagExecutor) buildExecutionLevels(steps []WorkflowStep) ([]*executionLevel, error) {
	// Build maps for efficient lookup
	stepMap := make(map[string]*WorkflowStep)
	for i := range steps {
		stepMap[steps[i].ID] = &steps[i]
	}

	// Build dependency graph: step -> list of steps that depend on it
	dependents := make(map[string][]string)
	inDegree := make(map[string]int)

	// Initialize in-degree for all steps
	for i := range steps {
		stepID := steps[i].ID
		inDegree[stepID] = 0

		// Initialize dependents map
		dependents[stepID] = []string{}
	}

	// Build the graph
	for i := range steps {
		step := &steps[i]
		for _, depID := range step.DependsOn {
			// Add to dependents list
			dependents[depID] = append(dependents[depID], step.ID)

			// Increment in-degree
			inDegree[step.ID]++
		}
	}

	// Perform level-by-level topological sort (Kahn's algorithm)
	var levels []*executionLevel
	processed := make(map[string]bool)

	for len(processed) < len(steps) {
		// Find all steps with in-degree 0 (no unmet dependencies)
		currentLevel := &executionLevel{
			steps: []*WorkflowStep{},
		}

		for stepID, degree := range inDegree {
			if degree == 0 && !processed[stepID] {
				currentLevel.steps = append(currentLevel.steps, stepMap[stepID])
				processed[stepID] = true
			}
		}

		// If no steps found, we have a cycle (this should be caught by validation)
		if len(currentLevel.steps) == 0 {
			return nil, fmt.Errorf("%w: topological sort failed - no steps with zero dependencies", ErrCircularDependency)
		}

		// Add level to result
		levels = append(levels, currentLevel)

		// Update in-degrees for next iteration
		for _, step := range currentLevel.steps {
			for _, dependentID := range dependents[step.ID] {
				inDegree[dependentID]--
			}
		}
	}

	return levels, nil
}

// getExecutionStats returns statistics about the execution plan.
func (*dagExecutor) getExecutionStats(levels []*executionLevel) map[string]int {
	stats := map[string]int{
		"total_levels":     len(levels),
		"total_steps":      0,
		"max_parallelism":  0,
		"min_parallelism":  0,
		"sequential_steps": 0, // Steps that must run alone
	}

	for _, level := range levels {
		levelSize := len(level.steps)
		stats["total_steps"] += levelSize

		if stats["max_parallelism"] == 0 || levelSize > stats["max_parallelism"] {
			stats["max_parallelism"] = levelSize
		}

		if stats["min_parallelism"] == 0 || levelSize < stats["min_parallelism"] {
			stats["min_parallelism"] = levelSize
		}

		if levelSize == 1 {
			stats["sequential_steps"]++
		}
	}

	return stats
}
