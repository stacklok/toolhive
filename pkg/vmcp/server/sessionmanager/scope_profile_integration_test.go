// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionmanager

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"

	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// A restore rewrites backend metadata in shared storage. Keep the initialize
// profile through that rewrite so subsequent replicas make the same decision.
func TestScopeProfileSurvivesRepeatedCrossReplicaRestoration(t *testing.T) {
	t.Parallel()
	storage := newSharedRedisStorage(t, miniredis.RunT(t))
	writer := newTestManagerWithSharedStorage(t, storage, nil)
	id := createSession(t, writer, nil)
	require.NoError(t, writer.DecorateSession(id, func(sess sessiontypes.MultiSession) sessiontypes.MultiSession {
		sess.SetMetadata(MetadataKeyClientName, "example-client")
		sess.SetMetadata(MetadataKeyClientVersion, "1.2.3")
		return sess
	}))
	for range 2 {
		reader := newTestManagerWithSharedStorage(t, storage, nil)
		sess, ok := reader.GetMultiSession(t.Context(), id)
		require.True(t, ok)
		require.Equal(t, "example-client", sess.GetMetadata()[MetadataKeyClientName])
		require.Equal(t, "1.2.3", sess.GetMetadata()[MetadataKeyClientVersion])
		metadata, err := storage.LoadMetadata(t.Context(), id)
		require.NoError(t, err)
		require.Equal(t, "example-client", metadata[MetadataKeyClientName])
		require.Equal(t, "1.2.3", metadata[MetadataKeyClientVersion])
	}
}
