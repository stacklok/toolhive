// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// newMemoryDCRStore is a test-only convenience constructor wrapping
// storage.NewMemoryStorage in the runner-side adapter. Production deployments
// do NOT reach this constructor — NewEmbeddedAuthServer type-asserts the
// shared authserver storage to storage.DCRCredentialStore and passes it to
// NewStorageBackedStore directly.
//
// The caller's *testing.T is required because storage.NewMemoryStorage
// launches a background cleanup goroutine on construction; the helper
// registers t.Cleanup(stor.Close) so each test releases the goroutine when
// it finishes. Without this every test that built a fresh store would leak a
// cleanupLoop goroutine for the duration of the test process.
func newMemoryDCRStore(t *testing.T) CredentialStore {
	t.Helper()
	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	return NewStorageBackedStore(stor)
}

// readTokenFile reads a secret from disk and returns its trimmed contents,
// mirroring the production embeddedauthserver.resolveSecret semantics so
// resolver tests can populate Request.InitialAccessToken from a file path.
func readTokenFile(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // G304: test-only helper, paths are constructed under t.TempDir()
	require.NoError(t, err)
	defer f.Close()
	data, err := io.ReadAll(f)
	require.NoError(t, err)
	// Trim trailing newline conventionally appended by Kubernetes mounts /
	// shell heredocs — same trim production code performs.
	for len(data) > 0 && (data[len(data)-1] == '\n' || data[len(data)-1] == ' ' || data[len(data)-1] == '\t' || data[len(data)-1] == '\r') {
		data = data[:len(data)-1]
	}
	return string(data)
}
