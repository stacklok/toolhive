// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validating

import (
	"os"
	"testing"

	"github.com/stacklok/toolhive/pkg/webhook"
)

// TestMain installs a permissive dialer control hook for the entire test
// binary so that webhook clients can dial httptest servers bound to 127.0.0.1.
// The production hook (networking.ProtectedDialerControl) would otherwise reject
// loopback addresses as part of the SSRF guard.
func TestMain(m *testing.M) {
	webhook.SetDialerControlForTestMain(webhook.AllowAnyDialerControl)
	os.Exit(m.Run())
}
