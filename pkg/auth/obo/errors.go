// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import "errors"

// ErrEnterpriseRequired is returned by every default OBO dispatch point — the
// controllerutil handler hook, the vMCP converter stub, and the middleware
// stub — when no out-of-tree handler/factory has been registered. Callers must
// use errors.Is to compare; the error wraps cleanly through
// fmt.Errorf("...: %w", ...).
//
// Lives in pkg/auth/obo (a leaf package) so that callers in
// cmd/thv-operator/... and pkg/vmcp/... can share the same sentinel without
// either layer importing the other. To register an out-of-tree handler, see
// controllerutil.RegisterOBOHandler (for the operator dispatch points) and
// obo.RegisterFactory (for the proxy middleware factory).
var ErrEnterpriseRequired = errors.New(
	"on-behalf-of (OBO) external auth type requires an enterprise build")

// ValidationError is the typed error an OBO handler returns when its input
// is genuinely malformed and the user must fix the spec for the failure to
// clear. It is the contract for the "permanent, user-fix" bucket in the
// OBOHandler error triage:
//
//   - errors.Is(err, ErrEnterpriseRequired) → not licensed; permanent until an
//     out-of-tree handler is registered.
//   - errors.As(err, &*ValidationError) → permanent until the user edits the
//     spec; the operator writes condition.Reason=InvalidConfig and does not
//     requeue.
//   - anything else → treated as transient by the reconciler, which returns
//     the error so controller-runtime requeues with backoff.
//
// Handler authors must use ValidationError for any condition the user can
// fix by editing the spec (missing field, malformed URL, schema violation)
// and must return a non-ValidationError for transient I/O failures (Secret
// not yet available, JWKS unreachable, webhook 5xx) so the reconciler
// retries instead of locking the resource into a permanent InvalidConfig
// state.
//
// The Message field is written verbatim into condition.Message — handler
// authors are responsible for ensuring it is safe to expose (no Secret
// names, no internal addressing, no credential fragments).
type ValidationError struct {
	Message string
}

// Error returns the user-facing message verbatim. The OBO branch in the
// MCPExternalAuthConfig reconciler writes this string into the Valid
// condition's Message field.
func (e *ValidationError) Error() string { return e.Message }
