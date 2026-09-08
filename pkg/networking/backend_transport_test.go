// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package networking

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCloneDefaultTransportWithDialControl verifies the shared backend
// transport construction: a nil control clones DefaultTransport (a distinct
// value that still reaches the server), and a non-nil control's hook fires on
// the dial so a deny-all hook blocks the request before it leaves the client.
func TestCloneDefaultTransportWithDialControl(t *testing.T) {
	t.Parallel()

	denyAll := func(_, _ string, _ syscall.RawConn) error {
		return errors.New("dial blocked by test policy")
	}

	t.Run("nil control clones a reachable transport", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		transport := CloneDefaultTransportWithDialControl(nil)
		require.NotNil(t, transport)
		assert.NotSame(t, http.DefaultTransport, transport, "must be a clone, not the shared DefaultTransport")

		resp, err := (&http.Client{Transport: transport}).Get(srv.URL)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, int32(1), hits.Load())
	})

	t.Run("non-nil control fires on the dial", func(t *testing.T) {
		t.Parallel()

		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(srv.Close)

		transport := CloneDefaultTransportWithDialControl(denyAll)
		_, err := (&http.Client{Transport: transport}).Get(srv.URL)
		require.Error(t, err, "deny-all dial control must block the request")
		assert.Zero(t, hits.Load(), "a blocked dial must not reach the server")
	})
}
