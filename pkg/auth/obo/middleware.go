// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package obo provides the proxy-runtime middleware factory hook for the
// on-behalf-of (OBO) external auth type. The default factory produces a
// stub middleware that responds 503 to every request. An out-of-tree build
// replaces the factory by calling RegisterFactory once during init().
package obo

import (
	"net/http"
	"sync"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type identifier used in MiddlewareConfig.Type for
// OBO middleware. Matches the ExternalAuthType constant value "obo".
const MiddlewareType = "obo"

// stubMessage is the body returned by the default 503 handler. It is shared
// between the default handler and tests in this package so the exact literal
// is only spelled once. Unexported because no caller outside this package has
// a use for it — tests reference it directly.
const stubMessage = "obo requires a build with a registered OBO middleware factory"

// stub is the default Middleware implementation. Its handler responds 503
// with a body explaining that the OBO middleware factory has not been
// registered. It is never instantiated except by the default CreateMiddleware
// factory, which an out-of-tree build replaces via RegisterFactory.
type stub struct{}

// Handler returns a MiddlewareFunction that wraps any downstream handler with
// a 503 response. The downstream handler is intentionally never called — the
// stub is the user-visible signal that the OBO middleware factory has not
// been registered.
func (*stub) Handler() types.MiddlewareFunction {
	return func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, stubMessage, http.StatusServiceUnavailable)
		})
	}
}

// Close is a no-op for the stub middleware.
func (*stub) Close() error { return nil }

// factoryMu guards reads and writes of currentFactory. Each call to
// CreateMiddleware takes the read lock; RegisterFactory takes the write lock.
// The mutex makes hot-reload / re-registration safe in case a future caller
// (e.g. an admission webhook) wires it up, even though production today only
// ever writes during init().
var factoryMu sync.RWMutex

// currentFactory holds the actual middleware factory dispatched to by
// CreateMiddleware. The default returns a 503 stub; an out-of-tree build
// replaces it via RegisterFactory.
var currentFactory types.MiddlewareFactory = DefaultFactory

// DefaultFactory adds a stub middleware whose handler responds 503 to every
// request. Exposed primarily so external test code (e.g. pkg/runner) can pass
// it to RegisterFactory in a t.Cleanup to restore the package's default
// behavior after a test mutates currentFactory.
func DefaultFactory(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	runner.AddMiddleware(config.Type, &stub{})
	return nil
}

// CreateMiddleware is the package-level middleware factory. It is a stable
// indirection over currentFactory: each call dispatches to whatever factory
// is registered at call time, so out-of-tree builds replacing the factory
// via RegisterFactory take effect on subsequent calls even if a caller has
// already captured CreateMiddleware itself (e.g. pkg/runner builds its
// factory map once and reuses it across runner instances). The default
// produces a 503 stub.
//
// Declared as a function (matching sibling middleware packages such as
// awssts, upstreamswap, and oauthproto/tokenexchange) so RegisterFactory is
// the only mutation path — there is no second escape hatch via direct
// assignment to CreateMiddleware.
func CreateMiddleware(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	factoryMu.RLock()
	f := currentFactory
	factoryMu.RUnlock()
	return f(config, runner)
}

// RegisterFactory replaces the underlying middleware factory. Calling it
// more than once is allowed and last-write-wins, matching the existing
// pkg/config.RegisterProviderFactory precedent. Panics if f is nil — a nil
// factory would dispatch into a nil function on the next CreateMiddleware
// call, far from the registration site; surface the problem at init() time
// instead.
func RegisterFactory(f types.MiddlewareFactory) {
	if f == nil {
		panic("obo.RegisterFactory: factory is nil")
	}
	factoryMu.Lock()
	defer factoryMu.Unlock()
	currentFactory = f
}
