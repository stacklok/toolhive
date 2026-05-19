// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package obo provides the proxy-runtime middleware factory hook for the
// on-behalf-of (OBO) external auth type. The default factory produces a
// stub middleware that responds 503 to every request. An out-of-tree build
// replaces the factory by calling RegisterFactory once during init().
package obo

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/transport/types"
)

// MiddlewareType is the type identifier used in MiddlewareConfig.Type for
// OBO middleware. Matches the ExternalAuthType constant value "obo".
const MiddlewareType = "obo"

// stubMessage is the body returned by the default 503 handler. It is exported
// at package scope so tests can match on it without duplicating the literal.
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

// CreateMiddleware is the package-level middleware factory. By default it
// adds a stub middleware whose handler returns 503 on every request. An
// out-of-tree build replaces it by calling RegisterFactory once during
// init(); the literal map in pkg/runner/middleware.go captures this
// variable's current value at runner-construction time, so all
// RegisterFactory calls must complete before the first runner is
// constructed (this is satisfied in practice because runner construction
// happens inside app.Run() after all init() functions have fired).
var CreateMiddleware types.MiddlewareFactory = func(config *types.MiddlewareConfig, runner types.MiddlewareRunner) error {
	runner.AddMiddleware(config.Type, &stub{})
	return nil
}

// RegisterFactory replaces the package-level CreateMiddleware. Calling it
// more than once is allowed and last-write-wins, matching the existing
// pkg/config.RegisterProviderFactory precedent.
func RegisterFactory(f types.MiddlewareFactory) { CreateMiddleware = f }
