// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

func TestMakeSession_PersistsBackendSessionIDs(t *testing.T) {
	t.Parallel()

	t.Run("two backends: both session IDs written to metadata", func(t *testing.T) {
		t.Parallel()

		connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, _ string) (internalbk.Session, *vmcp.CapabilityList, error) {
			ids := map[string]string{
				"backend-a": "sess-a",
				"backend-b": "sess-b",
			}
			sessID, ok := ids[target.WorkloadID]
			if !ok {
				return nil, nil, nil
			}
			return &mockConnectedBackend{sessID: sessID}, &vmcp.CapabilityList{}, nil
		}

		factory := newSessionFactoryWithConnector(connector)
		backends := []*vmcp.Backend{
			{ID: "backend-a"},
			{ID: "backend-b"},
		}
		sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, backends)
		require.NoError(t, err)

		meta := sess.GetMetadata()
		assert.Equal(t, "sess-a", meta[MetadataKeyBackendSessionPrefix+"backend-a"])
		assert.Equal(t, "sess-b", meta[MetadataKeyBackendSessionPrefix+"backend-b"])
		// MetadataKeyBackendIDs must still be written correctly.
		ids := strings.Split(meta[MetadataKeyBackendIDs], ",")
		assert.ElementsMatch(t, []string{"backend-a", "backend-b"}, ids)
	})

	t.Run("zero backends: no backend session keys written", func(t *testing.T) {
		t.Parallel()

		factory := newSessionFactoryWithConnector(nilBackendConnector())
		sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, nil)
		require.NoError(t, err)

		meta := sess.GetMetadata()
		for k := range meta {
			assert.False(t, strings.HasPrefix(k, MetadataKeyBackendSessionPrefix),
				"no backend session keys expected when no backends connected, got %q", k)
		}
		backendIDs, present := meta[MetadataKeyBackendIDs]
		assert.True(t, present, "MetadataKeyBackendIDs must always be written (empty string for zero backends)")
		assert.Empty(t, backendIDs, "MetadataKeyBackendIDs must be empty string when no backends connected")
	})

	t.Run("partial failure: only successful backend written", func(t *testing.T) {
		t.Parallel()

		connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, _ string) (internalbk.Session, *vmcp.CapabilityList, error) {
			if target.WorkloadID == "backend-ok" {
				return &mockConnectedBackend{sessID: "sess-ok"}, &vmcp.CapabilityList{}, nil
			}
			// backend-fail returns nil — skipped during init.
			return nil, nil, nil
		}

		factory := newSessionFactoryWithConnector(connector)
		backends := []*vmcp.Backend{
			{ID: "backend-ok"},
			{ID: "backend-fail"},
		}
		sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, backends)
		require.NoError(t, err)

		meta := sess.GetMetadata()
		assert.Equal(t, "sess-ok", meta[MetadataKeyBackendSessionPrefix+"backend-ok"])
		_, present := meta[MetadataKeyBackendSessionPrefix+"backend-fail"]
		assert.False(t, present, "failed backend must not have a session ID key")
	})

	t.Run("MetadataKeyBackendSessionPrefix constant value", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "vmcp.backend.session.", MetadataKeyBackendSessionPrefix)
	})
}

func TestRestoreSession_FreshlyPopulatesMetadataKeyBackendIDs(t *testing.T) {
	t.Parallel()

	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, _ string) (internalbk.Session, *vmcp.CapabilityList, error) {
		ids := map[string]string{
			"backend-a": "sess-a",
			"backend-b": "sess-b",
		}
		sessID, ok := ids[target.WorkloadID]
		if !ok {
			return nil, nil, nil
		}
		return &mockConnectedBackend{sessID: sessID}, &vmcp.CapabilityList{}, nil
	}

	factory := newSessionFactoryWithConnector(connector)
	backends := []*vmcp.Backend{
		{ID: "backend-a"},
		{ID: "backend-b"},
	}
	sessionID := "restore-test-session"

	// Create the initial session so we have a real token hash in metadata.
	original, err := factory.MakeSessionWithID(t.Context(), sessionID, nil, true, backends)
	require.NoError(t, err)
	t.Cleanup(func() { _ = original.Close() })

	// Simulate what storage looks like after NotifyBackendExpired ran for
	// backend-a: the per-backend session key is deleted and MetadataKeyBackendIDs
	// is trimmed to the remaining backend.
	storedMeta := original.GetMetadata() // returns a copy
	delete(storedMeta, MetadataKeyBackendSessionPrefix+"backend-a")
	storedMeta[MetadataKeyBackendIDs] = "backend-b"

	// RestoreSession must freshly compute MetadataKeyBackendIDs from the
	// backends that actually reconnect, not copy the stored value verbatim.
	// Passing both backends to allBackends mirrors how Manager.loadSession
	// calls factory.RestoreSession; filterBackendsByStoredIDs will filter to
	// just backend-b based on the trimmed MetadataKeyBackendIDs.
	restored, err := factory.RestoreSession(t.Context(), sessionID, storedMeta, backends)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })

	meta := restored.GetMetadata()
	assert.Equal(t, "backend-b", meta[MetadataKeyBackendIDs],
		"MetadataKeyBackendIDs must reflect only the backends that reconnected")
	_, expiredPresent := meta[MetadataKeyBackendSessionPrefix+"backend-a"]
	assert.False(t, expiredPresent,
		"expired backend-a must not appear in restored session metadata")
	assert.Equal(t, "sess-b", meta[MetadataKeyBackendSessionPrefix+"backend-b"],
		"surviving backend-b session key must be present")
}

func TestRestoreSession_AbsentMetadataKeyBackendIDsReturnsError(t *testing.T) {
	t.Parallel()

	factory := newSessionFactoryWithConnector(nilBackendConnector())

	// Metadata with no MetadataKeyBackendIDs key simulates corrupted or
	// placeholder storage that was never fully initialised.
	corrupted := map[string]string{}

	_, err := factory.RestoreSession(t.Context(), "some-session-id", corrupted, nil)
	require.Error(t, err, "absent MetadataKeyBackendIDs must return an error")
	assert.Contains(t, err.Error(), MetadataKeyBackendIDs,
		"error message must name the missing key")
}

// TestRestoreSession_PassesStoredSessionHintToConnector verifies that
// RestoreSession reads the per-backend session IDs stored in metadata and
// passes them as session hints to the backend connector, so backends can
// resume rather than re-initialize their sessions.
func TestRestoreSession_PassesStoredSessionHintToConnector(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	hintsReceived := map[string]string{}

	// connector records the session hint it receives for each backend.
	// It always returns a stable session ID so that the original session
	// has predictable per-backend metadata to store.
	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, sessionHint string) (internalbk.Session, *vmcp.CapabilityList, error) {
		mu.Lock()
		hintsReceived[target.WorkloadID] = sessionHint
		mu.Unlock()
		return &mockConnectedBackend{sessID: "orig-" + target.WorkloadID}, &vmcp.CapabilityList{}, nil
	}

	factory := newSessionFactoryWithConnector(connector)
	backends := []*vmcp.Backend{
		{ID: "backend-a"},
		{ID: "backend-b"},
	}

	// Create the original session — connector receives empty hints.
	original, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, backends)
	require.NoError(t, err)
	t.Cleanup(func() { _ = original.Close() })

	// Confirm original session stored per-backend session IDs in metadata.
	storedMeta := original.GetMetadata()
	storedHintA := storedMeta[MetadataKeyBackendSessionPrefix+"backend-a"]
	storedHintB := storedMeta[MetadataKeyBackendSessionPrefix+"backend-b"]
	require.NotEmpty(t, storedHintA, "original session must write backend-a session ID to metadata")
	require.NotEmpty(t, storedHintB, "original session must write backend-b session ID to metadata")

	// Reset captured hints before calling RestoreSession.
	mu.Lock()
	hintsReceived = map[string]string{}
	mu.Unlock()

	// RestoreSession must pass the stored session IDs as hints to the connector.
	restored, err := factory.RestoreSession(t.Context(), uuid.New().String(), storedMeta, backends)
	require.NoError(t, err)
	t.Cleanup(func() { _ = restored.Close() })

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, storedHintA, hintsReceived["backend-a"],
		"RestoreSession must pass stored backend-a session ID as hint to connector")
	assert.Equal(t, storedHintB, hintsReceived["backend-b"],
		"RestoreSession must pass stored backend-b session ID as hint to connector")
}

// TestMakeSession_PassesEmptySessionHintToConnector verifies that MakeSession
// (creating a new session, not restoring) passes an empty hint so that the
// backend always creates a fresh session.
func TestMakeSession_PassesEmptySessionHintToConnector(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	hintsReceived := map[string]string{}

	connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, sessionHint string) (internalbk.Session, *vmcp.CapabilityList, error) {
		mu.Lock()
		hintsReceived[target.WorkloadID] = sessionHint
		mu.Unlock()
		return &mockConnectedBackend{sessID: "new-sess"}, &vmcp.CapabilityList{}, nil
	}

	factory := newSessionFactoryWithConnector(connector)
	sess, err := factory.MakeSessionWithID(t.Context(), uuid.New().String(), nil, true, []*vmcp.Backend{{ID: "backend-a"}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })

	mu.Lock()
	defer mu.Unlock()
	assert.Empty(t, hintsReceived["backend-a"],
		"MakeSession must pass an empty session hint to the connector")
}
