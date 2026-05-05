// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"syscall"
	"testing"
)

// The functions in this file are test-only helpers that intentionally live in a
// non-_test.go file so that sub-package tests (e.g. pkg/webhook/validating,
// pkg/webhook/mutating) can call into them via TestMain. There is no clean
// alternative for cross-package test-time injection of the package-level
// dialerControl hook, so these helpers are exported. The testing.TB argument
// (or the explicit "ForTestMain" suffix) is the signal that the call is
// test-scoped; production code MUST NOT call any of them.

// SetDialerControlForTesting overrides the package-level dialerControl hook
// for the duration of tb. It is the sanctioned way for tests to bypass the
// production SSRF dial-time guard so they can talk to httptest servers,
// which always bind 127.0.0.1. The previous value is restored via t.Cleanup.
//
// Production code MUST NOT call this function. The testing.TB argument is the
// signal that the call is test-scoped.
func SetDialerControlForTesting(tb testing.TB, control func(network, address string, c syscall.RawConn) error) {
	tb.Helper()
	prev := dialerControl.Load()
	fn := dialerControlFunc(control)
	dialerControl.Store(&fn)
	tb.Cleanup(func() { dialerControl.Store(prev) })
}

// SetDialerControlForTestMain installs control as the dialer guard for the
// rest of the test binary's lifetime. Use this in TestMain in sub-packages
// whose entire test suite legitimately dials httptest servers bound to
// 127.0.0.1. There is no restore — the binary exits anyway.
//
// Production code MUST NOT call this function.
func SetDialerControlForTestMain(control func(network, address string, c syscall.RawConn) error) {
	fn := dialerControlFunc(control)
	dialerControl.Store(&fn)
}

// AllowAnyDialerControl is a permissive Control function for tests that
// need to dial httptest servers on 127.0.0.1. It performs no validation
// and always returns nil.
//
// Production code MUST NOT use this function.
func AllowAnyDialerControl(_, _ string, _ syscall.RawConn) error { return nil }
