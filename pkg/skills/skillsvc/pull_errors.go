// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"errors"
	"net/http"

	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// classifyPullError maps an error returned by Registry.Pull (which wraps
// oras.Copy) into an HTTP status code that best describes the failure to a
// caller. The classifier inspects:
//
//   - context.DeadlineExceeded / context.Canceled — mapped to 504 so callers
//     can distinguish upstream slowness from a registry-side rejection.
//   - *errcode.ErrorResponse (HTTP error from the remote registry) — mapped
//     by StatusCode: 401/403 → 401, 404 → 404, 429 → 429, other 4xx → 502,
//     5xx → 502.
//   - errdef.ErrNotFound — mapped to 404 (covers cases where oras surfaces
//     not-found as a sentinel rather than an ErrorResponse).
//
// Anything else is treated as a generic 502 Bad Gateway.
//
// The returned code is always in the 4xx or 5xx range; callers wrap the
// original error with httperr.WithCode(code) so the ErrorHandler renders an
// appropriate response.
func classifyPullError(err error) int {
	if err == nil {
		return http.StatusOK
	}

	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return http.StatusGatewayTimeout
	}

	var httpErr *errcode.ErrorResponse
	if errors.As(err, &httpErr) {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusUnauthorized
		case http.StatusNotFound:
			return http.StatusNotFound
		case http.StatusTooManyRequests:
			return http.StatusTooManyRequests
		}
		// Other 4xx and 5xx registry responses are treated as generic
		// upstream failures.
		return http.StatusBadGateway
	}

	if errors.Is(err, errdef.ErrNotFound) {
		return http.StatusNotFound
	}

	return http.StatusBadGateway
}
