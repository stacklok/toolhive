// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/vmcp"
	internalbk "github.com/stacklok/toolhive/pkg/vmcp/session/internal/backend"
)

// sinkRecorder records, per backend, whether the factory handed the connector a
// sink. That nil is the whole mechanism: the connector only opens a standalone
// notification stream when it has a sink to feed.
//
// initOneBackend runs one goroutine per backend, so the writes are concurrent
// and must be synchronised or -race fails the test.
type sinkRecorder struct {
	mu  sync.Mutex
	got map[string]bool
}

func newSinkRecorder() *sinkRecorder {
	return &sinkRecorder{got: make(map[string]bool)}
}

func (r *sinkRecorder) connector() backendConnector {
	return func(
		_ context.Context, target *vmcp.BackendTarget, _ *auth.Identity, _ string, sink internalbk.ListChangedSink,
	) (internalbk.Session, *vmcp.CapabilityList, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.got[target.WorkloadID] = sink != nil
		return &mockConnectedBackend{}, &vmcp.CapabilityList{}, nil
	}
}

func (r *sinkRecorder) snapshot() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.got))
	for k, v := range r.got {
		out[k] = v
	}
	return out
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

			rec := newSinkRecorder()
			factory := newSessionFactoryWithConnector(rec.connector(), tt.opts...)
			sess, err := factory.MakeSessionWithID(
				context.Background(), uuid.New().String(), nil, backends, noopSink,
			)
			require.NoError(t, err)
			require.NotNil(t, sess)
			assert.Equal(t, tt.want, rec.snapshot())
			require.NoError(t, sess.Close())
		})
	}
}

// A caller that passes no sink stays unsubscribed regardless of the filter.
func TestSessionFactory_ListChangedFilter_NilSinkUnaffected(t *testing.T) {
	t.Parallel()

	rec := newSinkRecorder()
	factory := newSessionFactoryWithConnector(
		rec.connector(),
		WithListChangedFilter(func(string) bool { return true }),
	)
	backends := []*vmcp.Backend{{ID: "b", Name: "b", BaseURL: "http://x:9", TransportType: "streamable-http"}}

	sess, err := factory.MakeSessionWithID(context.Background(), uuid.New().String(), nil, backends, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)
	assert.Equal(t, map[string]bool{"b": false}, rec.snapshot())
	require.NoError(t, sess.Close())
}
