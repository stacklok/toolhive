// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"testing"

	"github.com/stacklok/toolhive/pkg/auth/dcr"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// newMemoryDCRStore is a test-only convenience constructor wrapping
// storage.NewMemoryStorage in the dcr-package adapter. Production deployments
// do NOT reach this constructor — NewEmbeddedAuthServer type-asserts the
// shared authserver storage to storage.DCRCredentialStore and passes it to
// dcr.NewStorageBackedStore directly.
//
// The caller's *testing.T is required because storage.NewMemoryStorage
// launches a background cleanup goroutine on construction; the helper
// registers t.Cleanup(stor.Close) so each test releases the goroutine when
// it finishes. Without this every test that built a fresh store would leak a
// cleanupLoop goroutine for the duration of the test process.
//
// A near-identical helper lives in pkg/auth/dcr/testhelpers_test.go for
// tests inside that package; the duplication is intentional because Go test
// helpers cannot be shared across packages without exporting them, and the
// cleanup-aware shape is too narrow to justify a public API surface.
func newMemoryDCRStore(t *testing.T) dcr.CredentialStore {
	t.Helper()
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	return dcr.NewStorageBackedStore(stor)
}
