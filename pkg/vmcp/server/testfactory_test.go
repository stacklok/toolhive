// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
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
	_ context.Context, _ string, _ *auth.Identity, _ bool, _ []*vmcp.Backend,
) (vmcpsession.MultiSession, error) {
	return nil, fmt.Errorf("minimalTestFactory: MakeSessionWithID not implemented in test helper")
}
