// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package validating

import (
	"os"
	"testing"

	"github.com/stacklok/toolhive/pkg/webhook"
)

// TestMain disables the webhook SSRF dial-time guard for the entire test
// binary so that webhook clients can dial httptest servers bound to 127.0.0.1.
// The production guard (networking.ProtectedDialerControl) would otherwise
// reject loopback addresses.
func TestMain(m *testing.M) {
	webhook.SetAllowPrivateIPsForTestMain()
	os.Exit(m.Run())
}
