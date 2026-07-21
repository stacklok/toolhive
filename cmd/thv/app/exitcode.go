// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import "errors"

// Exit codes for thv skill sync/upgrade, per RFC THV-0080's CI/scripting
// contract. 0 (success) and 1 (generic/unclassified error) are Go and
// cobra's own defaults and are not represented here.
const (
	// ExitCodeCheckFailure means sync --check or upgrade --fail-on-changes
	// found drift/available changes: the project does not match its lock
	// file, but nothing was installed, written, or removed.
	ExitCodeCheckFailure = 2
	// ExitCodePartialFailure means some, but not all, of the targeted
	// skills failed during sync or upgrade; check the reported outcomes for
	// which ones.
	ExitCodePartialFailure = 3
	// ExitCodePolicyRejection means the operation was refused by policy
	// rather than attempted and failed: a non-interactive sync/upgrade
	// declined the pre-install confirmation gate without --yes, or a
	// ref/signer-change guard blocked without its override flag.
	ExitCodePolicyRejection = 4
)

// exitCodeError pairs an error with the process exit code main() should use
// for it, so business logic in a RunE can request a specific exit code
// without main() needing to know the semantics of every command's errors.
type exitCodeError struct {
	err  error
	code int
}

// withExitCode wraps err so ExitCodeFromError reports code for it. Returns
// nil if err is nil, so callers can write `return withExitCode(err, ...)`
// unconditionally after an operation that may or may not have failed.
func withExitCode(err error, code int) error {
	if err == nil {
		return nil
	}
	return &exitCodeError{err: err, code: code}
}

func (e *exitCodeError) Error() string { return e.err.Error() }
func (e *exitCodeError) Unwrap() error { return e.err }

// ExitCodeFromError returns the process exit code for err: 0 for nil, the
// code carried by an exitCodeError (see withExitCode) if err wraps one,
// otherwise 1 — the generic failure code cobra callers already expect.
func ExitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var ece *exitCodeError
	if errors.As(err, &ece) {
		return ece.code
	}
	return 1
}
