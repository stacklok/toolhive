// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import "testing"

// The functions in this file are test-only helpers that intentionally live in a
// non-_test.go file so that sub-package tests (e.g. pkg/webhook/validating,
// pkg/webhook/mutating) can call into them via TestMain. There is no clean
// alternative for cross-package test-time injection of the package-level
// allowPrivateIPsForTesting flag, so these helpers are exported. The testing.TB
// argument (or the explicit "ForTestMain" suffix) is the signal that the call
// is test-scoped; production code MUST NOT call any of them.

// SetAllowPrivateIPsForTesting disables the webhook SSRF dial-time guard for
// the duration of tb. It is the sanctioned way for tests to let webhook
// clients dial httptest servers, which always bind 127.0.0.1. The previous
// value is restored via t.Cleanup.
//
// Production code MUST NOT call this function. The testing.TB argument is the
// signal that the call is test-scoped.
func SetAllowPrivateIPsForTesting(tb testing.TB) {
	tb.Helper()
	prev := allowPrivateIPsForTesting.Swap(true)
	tb.Cleanup(func() { allowPrivateIPsForTesting.Store(prev) })
}

// SetAllowPrivateIPsForTestMain disables the webhook SSRF dial-time guard for
// the rest of the test binary's lifetime. Use this in TestMain in sub-packages
// whose entire test suite legitimately dials httptest servers bound to
// 127.0.0.1. There is no restore — the binary exits anyway.
//
// Production code MUST NOT call this function.
func SetAllowPrivateIPsForTestMain() {
	allowPrivateIPsForTesting.Store(true)
}
