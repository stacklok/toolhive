// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import "errors"

// ErrEnterpriseRequired was returned by the OBO dispatch points before OBO
// shipped in ToolHive itself. No code path in this module returns it any more.
//
// Deprecated: OBO is built in. The sentinel is kept only so out-of-tree code
// that still references it keeps compiling; it will be removed once
// out-of-tree users are gone (see stacklok/toolhive#7200).
var ErrEnterpriseRequired = errors.New(
	"on-behalf-of (OBO) external auth type requires an enterprise build")

// ValidationError is the typed error OBO validation returns when its input is
// genuinely malformed and the user must fix the spec for the failure to clear.
// It is the contract for the "permanent, user-fix" bucket the operator's OBO
// error triage applies:
//
//   - errors.As(err, &*ValidationError): permanent until the user edits the
//     spec; the operator writes condition.Reason=InvalidConfig and does not
//     requeue.
//   - anything else: treated as transient by the reconciler, which returns the
//     error so controller-runtime requeues with backoff.
//
// Use ValidationError for any condition the user can fix by editing the spec
// (missing field, malformed URL, schema violation), and a plain error for
// transient I/O failures (Secret not yet available) so the reconciler retries
// instead of locking the resource into a permanent InvalidConfig state.
//
// The Message field is written verbatim into condition.Message, so it must be
// safe to expose (no Secret names, no internal addressing, no credential
// fragments).
type ValidationError struct {
	Message string
}

// Error returns the user-facing message verbatim.
func (e *ValidationError) Error() string { return e.Message }
