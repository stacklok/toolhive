// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// newMemoryDCRStore returns the independent operation-scoped resolver store.
func newMemoryDCRStore(t *testing.T) CredentialStore {
	t.Helper()
	store := NewInMemoryStore()
	t.Cleanup(func() { _ = store.Close() })
	return store
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
