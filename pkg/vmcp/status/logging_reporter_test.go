// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
)

func TestLoggingReporter_ReportStatus(t *testing.T) {
	t.Parallel()

	reporter := NewLoggingReporter()
	ctx := context.Background()

	status := &vmcptypes.Status{
		Phase:   vmcptypes.PhaseReady,
		Message: "Server is ready",
		DiscoveredBackends: []vmcptypes.DiscoveredBackend{
			{
				Name:   "backend1",
				URL:    "http://backend1:8080",
				Status: vmcptypes.BackendHealthy.ToCRDStatus(),
			},
		},
		Timestamp: time.Now(),
	}

	require.NoError(t, reporter.ReportStatus(ctx, status))
}

func TestLoggingReporter_StartStop(t *testing.T) {
	t.Parallel()

	reporter := NewLoggingReporter()
	ctx := context.Background()

	shutdown, err := reporter.Start(ctx)
	require.NoError(t, err)
	require.NotNil(t, shutdown)

	require.NoError(t, shutdown(ctx))
}

func TestLoggingReporter_NilStatus(t *testing.T) {
	t.Parallel()

	reporter := NewLoggingReporter()
	ctx := context.Background()

	require.NoError(t, reporter.ReportStatus(ctx, nil))
}
