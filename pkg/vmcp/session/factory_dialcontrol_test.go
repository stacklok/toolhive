// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"errors"
	"syscall"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// TestSessionFactory_WithDialControl_GuardsSessionInit proves the option is
// threaded from the public NewSessionFactory API down to the per-backend
// session-init dial: with a deny-all control the real backend is never reached,
// so the (partial-failure) session is created but holds no capabilities. The
// unguarded TestSessionFactory_Integration_CapabilityDiscovery shows the same
// backend IS reached and discovered without the option.
func TestSessionFactory_WithDialControl_GuardsSessionInit(t *testing.T) {
	t.Parallel()

	baseURL := startInProcessMCPServer(t)
	backend := &vmcp.Backend{
		ID:            "guarded-backend",
		Name:          "guarded-backend",
		BaseURL:       baseURL,
		TransportType: "streamable-http",
	}

	factory := NewSessionFactory(newUnauthenticatedRegistry(t),
		WithDialControl(func(_, _ string, _ syscall.RawConn) error {
			return errors.New("dial blocked by test policy")
		}))

	// A blocked backend is a partial failure: the backend is excluded and the
	// session is still created, just with no held connection or capabilities.
	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, []*vmcp.Backend{backend}, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	assert.Empty(t, sess.Tools(), "guarded session-init dial must not discover backend tools")
	assert.Empty(t, sess.Resources(), "guarded session-init dial must not discover backend resources")
	assert.Empty(t, sess.Prompts(), "guarded session-init dial must not discover backend prompts")
}
