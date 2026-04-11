// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"sync"
)

// PolicyGate is called before server creation operations to allow external
// policy enforcement. Additional methods (e.g., CheckStopServer) may be added
// in future issues; downstream implementations should embed a NoopPolicyGate
// to remain forward-compatible.
type PolicyGate interface {
	// CheckCreateServer is called before a local workload container is set up.
	// Return a non-nil error to block server creation.
	CheckCreateServer(ctx context.Context, cfg *RunConfig) error
}

// NoopPolicyGate is a policy gate that allows all operations. Downstream
// implementations should embed this struct to remain forward-compatible when
// new methods are added to the PolicyGate interface.
type NoopPolicyGate struct{}

// CheckCreateServer implements PolicyGate by allowing all create operations.
func (NoopPolicyGate) CheckCreateServer(_ context.Context, _ *RunConfig) error {
	return nil
}

// allowAllGate is the default policy gate used when no gate has been registered.
type allowAllGate struct {
	NoopPolicyGate
}

var (
	policyGateMu sync.RWMutex
	policyGate   PolicyGate = allowAllGate{}
)

// RegisterPolicyGate replaces the active policy gate with g. It is safe to
// call from multiple goroutines, though it is intended to be called once at
// program startup before any runners are created.
func RegisterPolicyGate(g PolicyGate) {
	policyGateMu.Lock()
	defer policyGateMu.Unlock()
	policyGate = g
}

// ActivePolicyGate returns the currently registered policy gate under the
// package-level mutex. It is exported for use by other toolhive packages
// (e.g. retriever) that enforce policy outside Runner.Run; it is not
// intended for external consumers.
func ActivePolicyGate() PolicyGate {
	policyGateMu.RLock()
	defer policyGateMu.RUnlock()
	return policyGate
}
