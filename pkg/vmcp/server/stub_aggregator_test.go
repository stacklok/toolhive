// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server_test

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

// stubAggregator is a drop-in aggregator.Aggregator for server tests that route through
// core.New (which requires a non-nil Aggregator). Now that server.New delegates to
// core.New + Serve, the core is the single source of the advertised capability set, so
// tests provide the tools the core should advertise here rather than through the session
// factory.
//
// AggregateCapabilities — the only method the core invokes — returns a fixed result; the
// other interface methods are unreachable on the New/Serve path and fail loudly if called.
type stubAggregator struct {
	caps *aggregator.AggregatedCapabilities
}

var _ aggregator.Aggregator = (*stubAggregator)(nil)

// newStubAggregator returns a stub whose AggregateCapabilities yields caps. A nil caps
// yields an empty (no-capability) result, which is all transport-only tests need.
func newStubAggregator(caps *aggregator.AggregatedCapabilities) *stubAggregator {
	if caps == nil {
		caps = &aggregator.AggregatedCapabilities{}
	}
	return &stubAggregator{caps: caps}
}

func (s *stubAggregator) AggregateCapabilities(
	context.Context, []vmcp.Backend,
) (*aggregator.AggregatedCapabilities, error) {
	return s.caps, nil
}

func (*stubAggregator) QueryCapabilities(context.Context, vmcp.Backend) (*aggregator.BackendCapabilities, error) {
	panic("stubAggregator.QueryCapabilities: unexpected call on the New/Serve path")
}

func (*stubAggregator) QueryAllCapabilities(
	context.Context, []vmcp.Backend,
) (map[string]*aggregator.BackendCapabilities, error) {
	panic("stubAggregator.QueryAllCapabilities: unexpected call on the New/Serve path")
}

func (*stubAggregator) ResolveConflicts(
	context.Context, map[string]*aggregator.BackendCapabilities,
) (*aggregator.ResolvedCapabilities, error) {
	panic("stubAggregator.ResolveConflicts: unexpected call on the New/Serve path")
}

func (*stubAggregator) MergeCapabilities(
	context.Context, *aggregator.ResolvedCapabilities, vmcp.BackendRegistry,
) (*aggregator.AggregatedCapabilities, error) {
	panic("stubAggregator.MergeCapabilities: unexpected call on the New/Serve path")
}

func (*stubAggregator) ProcessPreQueriedCapabilities(
	context.Context, map[string][]vmcp.Tool, map[string]*vmcp.BackendTarget,
) ([]vmcp.Tool, []vmcp.Tool, map[string]*vmcp.BackendTarget, error) {
	panic("stubAggregator.ProcessPreQueriedCapabilities: unexpected call on the New/Serve path")
}
