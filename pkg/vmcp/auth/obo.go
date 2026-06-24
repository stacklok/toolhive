// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/auth/obo"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
)

// oboStrategyStub is the default OBO Strategy. Every method returns an error
// wrapping obo.ErrEnterpriseRequired. It is replaced at process start by an
// out-of-tree build calling RegisterOBOStrategy.
type oboStrategyStub struct{}

// Name returns the strategy identifier for OBO.
func (*oboStrategyStub) Name() string { return authtypes.StrategyTypeOBO }

// Authenticate returns obo.ErrEnterpriseRequired — the default build has no
// real OBO executor. An out-of-tree build registers a real strategy via
// RegisterOBOStrategy.
func (*oboStrategyStub) Authenticate(_ context.Context, _ *http.Request, _ *authtypes.BackendAuthStrategy) error {
	return fmt.Errorf("vMCP OBO strategy: %w", obo.ErrEnterpriseRequired)
}

// Validate returns obo.ErrEnterpriseRequired for the same reason as Authenticate.
func (*oboStrategyStub) Validate(_ *authtypes.BackendAuthStrategy) error {
	return fmt.Errorf("vMCP OBO strategy: %w", obo.ErrEnterpriseRequired)
}

// oboMu guards reads and writes of currentOBOStrategyFactory. Each call to
// NewOBOStrategy takes the read lock; RegisterOBOStrategy takes the write
// lock. Production today registers once during init(), but the lock makes
// re-registration safe for hot-reload / test scenarios.
var oboMu sync.RWMutex

// currentOBOStrategyFactory is the package-level OBO strategy factory.
// The default returns the stub; an out-of-tree build replaces it via
// RegisterOBOStrategy. Access only through NewOBOStrategy (read) or
// RegisterOBOStrategy (write) so the mutex contract is preserved.
var currentOBOStrategyFactory = func(_ env.Reader) Strategy {
	return &oboStrategyStub{}
}

// RegisterOBOStrategy replaces the package-level OBO strategy factory. It is
// intended to be called exactly once during init() in an out-of-tree package
// that blank-imports pkg/vmcp/auth.
//
// Calling it more than once is allowed and last-write-wins, matching the
// existing obo.RegisterFactory and controllerutil.RegisterOBOHandler
// precedents. Panics if f is nil — a nil factory would nil-deref on the next
// NewOBOStrategy call, far from the registration site; surfacing the problem
// at init() time is far easier to diagnose than at request time.
//
// The replacement is only effective if the running vMCP binary links the
// package that calls RegisterOBOStrategy. An out-of-tree binary must
// blank-import the overlay package from its cmd/vmcp entrypoint; the upstream
// cmd/vmcp does not.
func RegisterOBOStrategy(f func(env.Reader) Strategy) {
	if f == nil {
		panic("auth.RegisterOBOStrategy: factory is nil")
	}
	oboMu.Lock()
	defer oboMu.Unlock()
	currentOBOStrategyFactory = f
}

// NewOBOStrategy returns the OBO Strategy produced by the currently registered
// factory. With the default factory it returns the stub (whose Authenticate
// and Validate return obo.ErrEnterpriseRequired). With a registered out-of-tree
// factory it returns the real Entra OBO executor.
//
// Called by factory.NewOutgoingAuthRegistry when building the registry; the
// returned strategy is registered once and reused for all subsequent requests.
func NewOBOStrategy(envReader env.Reader) Strategy {
	oboMu.RLock()
	f := currentOBOStrategyFactory
	oboMu.RUnlock()
	return f(envReader)
}
