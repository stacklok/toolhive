// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"errors"
	"fmt"
)

// Exit codes for skill lock operations (RFC THV-0080).
const (
	ExitCodeCheckFailure    = 2
	ExitCodePartialFailure  = 3
	ExitCodePolicyRejection = 4
)

// exitCodeError carries a specific process exit code for the CLI.
type exitCodeError struct {
	code int
	err  error
}

func (e *exitCodeError) Error() string {
	if e.err == nil {
		return fmt.Sprintf("exit code %d", e.code)
	}
	return e.err.Error()
}

func (e *exitCodeError) Unwrap() error {
	return e.err
}

func newExitCodeError(code int, err error) error {
	if err == nil {
		return &exitCodeError{code: code}
	}
	return &exitCodeError{code: code, err: err}
}

// ExitCodeFromError returns a custom exit code when err wraps exitCodeError.
func ExitCodeFromError(err error) (int, bool) {
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code, true
	}
	return 1, false
}
