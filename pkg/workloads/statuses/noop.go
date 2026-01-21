// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package statuses

import (
	"context"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
)

// NoopStatusManager is a no-op implementation of StatusManager that does nothing.
// All methods return zero values or empty results without performing any operations.
type NoopStatusManager struct{}

// NewNoopStatusManager creates a new NoopStatusManager instance.
func NewNoopStatusManager() StatusManager {
	return &NoopStatusManager{}
}

// GetWorkload returns an empty workload and nil error.
func (*NoopStatusManager) GetWorkload(_ context.Context, _ string) (core.Workload, error) {
	return core.Workload{}, nil
}

// ListWorkloads returns an empty slice of workloads.
func (*NoopStatusManager) ListWorkloads(_ context.Context, _ bool, _ []string) ([]core.Workload, error) {
	return []core.Workload{}, nil
}

// SetWorkloadStatus does nothing and returns nil.
func (*NoopStatusManager) SetWorkloadStatus(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
	return nil
}

// DeleteWorkloadStatus does nothing and returns nil.
func (*NoopStatusManager) DeleteWorkloadStatus(_ context.Context, _ string) error {
	return nil
}

// SetWorkloadPID does nothing and returns nil.
func (*NoopStatusManager) SetWorkloadPID(_ context.Context, _ string, _ int) error {
	return nil
}

// ResetWorkloadPID does nothing and returns nil.
func (*NoopStatusManager) ResetWorkloadPID(_ context.Context, _ string) error {
	return nil
}

// GetWorkloadPID returns 0 and nil error.
func (*NoopStatusManager) GetWorkloadPID(_ context.Context, _ string) (int, error) {
	return 0, nil
}
