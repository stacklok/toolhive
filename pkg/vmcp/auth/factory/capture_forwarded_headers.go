// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"net/http"

	"github.com/stacklok/toolhive/pkg/auth"
)

// captureForwardedHeadersMiddleware returns HTTP middleware that copies
// allowlisted incoming request headers onto the request identity so they travel
// (via the identity object as an explicit parameter) into the per-session backend
// client.
//
// Design notes — why this is anti-pattern clean:
//   - The capture is part of the auth boundary's own job: it runs inside
//     composedAuth, the same middleware that calls auth.WithIdentity to establish
//     identity in the first place.
//   - It clones the identity (shallow struct copy) rather than mutating it in
//     place, satisfying the immutability rule in vmcp-anti-patterns.md §7 and
//     identityRoundTripper's contract.
//   - Downstream consumers receive the identity as an explicit parameter
//     (MakeSessionWithID) — this does NOT introduce the context-coupling
//     anti-pattern (§1) because no business-logic handler reads ForwardedHeaders
//     out of the context directly.
//
// If allow is empty the function returns a no-op wrapper to avoid allocations on
// the hot path.
func captureForwardedHeadersMiddleware(allow []string) func(http.Handler) http.Handler {
	if len(allow) == 0 {
		return func(next http.Handler) http.Handler { return next }
	}

	// Precompute the canonical header names once at construction time so the
	// per-request handler does only cheap map lookups.
	canonical := make([]string, len(allow))
	for i, name := range allow {
		canonical[i] = http.CanonicalHeaderKey(name)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.IdentityFromContext(r.Context())
			if !ok || id == nil {
				// No identity yet (or anonymous path with no identity) — pass through.
				next.ServeHTTP(w, r)
				return
			}

			// Collect the values of allowlisted headers that are actually present.
			fwd := make(map[string]string, len(canonical))
			for _, name := range canonical {
				if v := r.Header.Get(name); v != "" {
					fwd[name] = v
				}
			}

			if len(fwd) == 0 {
				// Nothing to attach — skip the clone to keep the fast path cheap.
				next.ServeHTTP(w, r)
				return
			}

			// Clone the identity (shallow struct copy) and attach the headers.
			// Never mutate the original — see vmcp-anti-patterns.md §7.
			clone := *id
			clone.ForwardedHeaders = fwd
			ctx := auth.WithIdentity(r.Context(), &clone)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
