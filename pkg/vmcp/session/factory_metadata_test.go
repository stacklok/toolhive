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

func TestMakeSession_PersistsBackendSessionIDs(t *testing.T) {
	t.Parallel()

	t.Run("two backends: both session IDs written to metadata", func(t *testing.T) {
		t.Parallel()

		connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
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

		connector := func(_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity) (internalbk.Session, *vmcp.CapabilityList, error) {
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
