// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatelessMCPPinger_Ping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		serverFunc  func(w http.ResponseWriter, r *http.Request)
		wantErr     bool
		wantHealthy bool // true = nil error, positive duration
	}{
		{
			name: "200 OK is healthy",
			serverFunc: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantErr:     false,
			wantHealthy: true,
		},
		{
			name: "401 unauthorized is treated as healthy",
			serverFunc: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			wantErr:     false,
			wantHealthy: true,
		},
		{
			name: "403 forbidden is treated as healthy",
			serverFunc: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			wantErr:     false,
			wantHealthy: true,
		},
		{
			name: "500 server error returns an error",
			serverFunc: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(tc.serverFunc))
			defer srv.Close()

			pinger := NewStatelessMCPPinger(srv.URL)
			duration, err := pinger.Ping(context.Background())

			if tc.wantErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			assert.Positive(t, duration, "duration should be positive on success")
		})
	}
}

func TestStatelessMCPPinger_Ping_ConnectionRefused(t *testing.T) {
	t.Parallel()

	// Point at a port where nothing is listening. Use a server, start it,
	// close it immediately so the port is definitely not in use.
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	addr := srv.URL
	srv.Close()

	pinger := NewStatelessMCPPingerWithTimeout(addr, 2*time.Second)
	_, err := pinger.Ping(context.Background())
	require.Error(t, err, "should return error when connection is refused")
}

func TestStatelessMCPPinger_Ping_UsesPost(t *testing.T) {
	t.Parallel()

	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pinger := NewStatelessMCPPinger(srv.URL)
	_, err := pinger.Ping(context.Background())
	require.NoError(t, err)

	assert.Equal(t, http.MethodPost, receivedMethod, "pinger should use POST method")
}

func TestStatelessMCPPinger_Ping_SendsJsonBody(t *testing.T) {
	t.Parallel()

	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		err = json.Unmarshal(raw, &body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	pinger := NewStatelessMCPPinger(srv.URL)
	_, err := pinger.Ping(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "2.0", body["jsonrpc"], "body should contain jsonrpc field")
	assert.Equal(t, "ping", body["method"], "body should contain method field")
	_, hasID := body["id"]
	assert.True(t, hasID, "body should contain id field")
}

func TestNewStatelessMCPPingerWithTimeout_ZeroTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Zero timeout should be replaced by DefaultPingerTimeout — the pinger
	// must still work (i.e., not time out immediately on a live server).
	pinger := NewStatelessMCPPingerWithTimeout(srv.URL, 0)
	_, err := pinger.Ping(context.Background())
	require.NoError(t, err, "pinger with zero timeout should default to DefaultPingerTimeout and succeed")

	// Verify the underlying client has the default timeout set.
	sp, ok := pinger.(*StatelessMCPPinger)
	require.True(t, ok, "pinger should be *StatelessMCPPinger")
	assert.Equal(t, DefaultPingerTimeout, sp.client.Timeout)
}
