// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"sync"

	"github.com/stacklok/toolhive-core/env"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/strategies"
)

// oboMu guards reads and writes of oboStrategyOverride.
var oboMu sync.RWMutex

// oboStrategyOverride is the factory registered through the deprecated
// RegisterOBOStrategy, or nil when none was registered.
var oboStrategyOverride func(env.Reader) Strategy

// RegisterOBOStrategy replaces the strategy NewOBOStrategy returns.
// Last-write-wins. Panics if f is nil.
//
// Deprecated: OBO is built in; NewOBOStrategy returns the Entra OBO strategy
// from pkg/vmcp/auth/strategies. RegisterOBOStrategy is kept only so
// out-of-tree code that still calls it keeps compiling; it will be removed in
// a future release.
func RegisterOBOStrategy(f func(env.Reader) Strategy) {
	if f == nil {
		panic("auth.RegisterOBOStrategy: factory is nil")
	}
	oboMu.Lock()
	defer oboMu.Unlock()
	oboStrategyOverride = f
}

// NewOBOStrategy returns the vMCP outgoing-auth strategy for on-behalf-of
// (OBO): the built-in Entra OBO strategy, or the strategy produced by a factory
// registered through the deprecated RegisterOBOStrategy.
//
// Called by factory.NewOutgoingAuthRegistry when building the registry; the
// returned strategy is registered once and reused for all requests.
func NewOBOStrategy(envReader env.Reader) Strategy {
	oboMu.RLock()
	f := oboStrategyOverride
	oboMu.RUnlock()
	if f != nil {
		return f(envReader)
	}
	return strategies.NewOBOStrategy(envReader)
}
