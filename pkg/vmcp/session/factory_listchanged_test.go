// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

// sinkRecordingConnector reports, per backend, whether the factory handed the
// connector a sink. A nil sink is what stops the connector opening a standalone
// notification stream against that backend.
func sinkRecordingConnector(got map[string]bool) backendConnector {
	return func(
		_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, _ string, sink internalbk.ListChangedSink,
	) (internalbk.Session, *vmcp.CapabilityList, error) {
		got[target.WorkloadID] = sink != nil
		return &mockConnectedBackend{}, &vmcp.CapabilityList{}, nil
	}
}

func TestSessionFactory_ListChangedFilter(t *testing.T) {
	t.Parallel()

	backends := []*vmcp.Backend{
		{ID: "keep", Name: "keep", BaseURL: "http://x:9", TransportType: "streamable-http"},
		{ID: "drop", Name: "drop", BaseURL: "http://x:9", TransportType: "streamable-http"},
	}
	noopSink := func(context.Context, string, ChangeKind) {}

	tests := []struct {
		name string
		opts []MultiSessionFactoryOption
		want map[string]bool
	}{
		{
			name: "no filter subscribes every backend",
			want: map[string]bool{"keep": true, "drop": true},
		},
		{
			name: "filter drops only the excluded backend",
			opts: []MultiSessionFactoryOption{
				WithListChangedFilter(func(id string) bool { return id != "drop" }),
			},
			want: map[string]bool{"keep": true, "drop": false},
		},
		{
			name: "filter can exclude everything",
			opts: []MultiSessionFactoryOption{
				WithListChangedFilter(func(string) bool { return false }),
			},
			want: map[string]bool{"keep": false, "drop": false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := map[string]bool{}
			factory := newSessionFactoryWithConnector(sinkRecordingConnector(got), tt.opts...)
			sess, err := factory.MakeSessionWithID(
				context.Background(), uuid.New().String(), nil, backends, noopSink,
			)
			require.NoError(t, err)
			require.NotNil(t, sess)
			assert.Equal(t, tt.want, got)
			require.NoError(t, sess.Close())
		})
	}
}

// A caller that passes no sink stays unsubscribed regardless of the filter.
func TestSessionFactory_ListChangedFilter_NilSinkUnaffected(t *testing.T) {
	t.Parallel()

	got := map[string]bool{}
	factory := newSessionFactoryWithConnector(
		sinkRecordingConnector(got),
		WithListChangedFilter(func(string) bool { return true }),
	)
	backends := []*vmcp.Backend{{ID: "b", Name: "b", BaseURL: "http://x:9", TransportType: "streamable-http"}}

	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, backends, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, map[string]bool{"b": false}, got)
	require.NoError(t, sess.Close())
}
