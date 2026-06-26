// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
)

// testMinimalFactory returns a minimal MultiSessionFactory for use in internal
// package server tests that need a non-nil SessionFactory but don't exercise
// session creation logic.
func testMinimalFactory() vmcpsession.MultiSessionFactory {
	return &minimalTestFactory{}
}

// minimalTestFactory is a no-op MultiSessionFactory that satisfies the
// vmcpsession.MultiSessionFactory interface.  Tests that accidentally trigger
// session creation will receive a clear error rather than a panic.
type minimalTestFactory struct{}

var _ vmcpsession.MultiSessionFactory = (*minimalTestFactory)(nil)

func (*minimalTestFactory) MakeSessionWithID(
	_ context.Context, _ string, _ *auth.Identity, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	return nil, fmt.Errorf("minimalTestFactory: MakeSessionWithID not implemented in test helper")
}

func (*minimalTestFactory) RestoreSession(
	_ context.Context, _ string, _ map[string]string, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	return nil, fmt.Errorf("minimalTestFactory: RestoreSession not implemented in test helper")
}

// stubAggregator is a drop-in aggregator.Aggregator for internal package server tests
// that route through core.New (which requires a non-nil Aggregator). AggregateCapabilities
// — the only method the core invokes — returns an empty result; the rest are unreachable on
// the New/Serve path and fail loudly if called. Internal tests only need the empty
// (no-capability) result; the external server_test stub (stub_aggregator_test.go) takes
// configurable caps.
type stubAggregator struct{}

var _ aggregator.Aggregator = (*stubAggregator)(nil)

func (*stubAggregator) AggregateCapabilities(
	context.Context, []vmcp.Backend,
) (*aggregator.AggregatedCapabilities, error) {
	return &aggregator.AggregatedCapabilities{}, nil
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
