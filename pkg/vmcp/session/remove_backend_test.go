// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

// makeSessionWithBackends creates a session via the factory wired to a
// connector that returns a non-empty sessID for each workload ID in sessIDs.
func makeSessionWithBackends(t *testing.T, workloadSessionIDs map[string]string) MultiSession {
	t.Helper()
	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
		sessID, ok := workloadSessionIDs[target.WorkloadID]
		if !ok {
			return nil, nil, nil
		}
		return &mockConnectedBackend{sessID: sessID}, &vmcp.CapabilityList{}, nil
	}
	factory := newSessionFactoryWithConnector(connector)
	backends := make([]*vmcp.Backend, 0, len(workloadSessionIDs))
	for id := range workloadSessionIDs {
		backends = append(backends, &vmcp.Backend{ID: id})
	}
	sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, backends)
	require.NoError(t, err)
	return sess
}

func TestRemoveBackendFromMetadata(t *testing.T) {
	t.Parallel()

	t.Run("single backend removed: key cleared, BackendIDs empty", func(t *testing.T) {
		t.Parallel()

		sess := makeSessionWithBackends(t, map[string]string{"backend-a": "sess-a"})
		sess.RemoveBackendFromMetadata("backend-a")

		meta := sess.GetMetadata()
		assert.Equal(t, "", meta[MetadataKeyBackendSessionPrefix+"backend-a"],
			"cleared backend session key must be empty string")
		assert.Equal(t, "", meta[MetadataKeyBackendIDs],
			"MetadataKeyBackendIDs must be empty when last backend removed")
	})

	t.Run("partial removal: cleared backend gone, survivor remains", func(t *testing.T) {
		t.Parallel()

		sess := makeSessionWithBackends(t, map[string]string{
			"backend-a": "sess-a",
			"backend-b": "sess-b",
		})
		sess.RemoveBackendFromMetadata("backend-a")

		meta := sess.GetMetadata()
		assert.Equal(t, "", meta[MetadataKeyBackendSessionPrefix+"backend-a"],
			"cleared backend session key must be empty string")
		assert.Equal(t, "sess-b", meta[MetadataKeyBackendSessionPrefix+"backend-b"],
			"surviving backend session key must be unchanged")
		assert.Equal(t, "backend-b", meta[MetadataKeyBackendIDs],
			"MetadataKeyBackendIDs must contain only the surviving backend")
	})

	t.Run("full removal: all backends gone", func(t *testing.T) {
		t.Parallel()

		sess := makeSessionWithBackends(t, map[string]string{
			"backend-a": "sess-a",
			"backend-b": "sess-b",
		})
		sess.RemoveBackendFromMetadata("backend-a")
		sess.RemoveBackendFromMetadata("backend-b")

		meta := sess.GetMetadata()
		assert.Equal(t, "", meta[MetadataKeyBackendSessionPrefix+"backend-a"])
		assert.Equal(t, "", meta[MetadataKeyBackendSessionPrefix+"backend-b"])
		assert.Equal(t, "", meta[MetadataKeyBackendIDs],
			"MetadataKeyBackendIDs must be empty after all backends removed")
	})

	t.Run("unknown workload ID is a no-op", func(t *testing.T) {
		t.Parallel()

		sess := makeSessionWithBackends(t, map[string]string{"backend-a": "sess-a"})
		before := sess.GetMetadata()[MetadataKeyBackendIDs]

		sess.RemoveBackendFromMetadata("does-not-exist")

		assert.Equal(t, before, sess.GetMetadata()[MetadataKeyBackendIDs],
			"MetadataKeyBackendIDs must be unchanged for unknown workload ID")
	})

	t.Run("MetadataKeyBackendIDs is sorted after partial removal", func(t *testing.T) {
		t.Parallel()

		sess := makeSessionWithBackends(t, map[string]string{
			"backend-c": "sess-c",
			"backend-a": "sess-a",
			"backend-b": "sess-b",
		})
		sess.RemoveBackendFromMetadata("backend-b")

		ids := strings.Split(sess.GetMetadata()[MetadataKeyBackendIDs], ",")
		assert.Equal(t, []string{"backend-a", "backend-c"}, ids,
			"remaining backend IDs must be sorted")
	})
}
