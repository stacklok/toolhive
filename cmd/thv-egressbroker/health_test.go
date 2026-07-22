// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
)

// healthFixture builds a health handler around a freshly generated CA and a
// configurable ping. The CA is valid (not rotation-due) at test time.
func healthFixture(t *testing.T, ping redisPinger) (*healthServer, *atomic.Bool) {
	t.Helper()
	ca, err := egressbroker.GenerateBumpCA("test", time.Now())
	require.NoError(t, err)
	var loaded atomic.Bool
	loaded.Store(true)
	if ping == nil {
		ping = func(context.Context) error { return nil }
	}
	return &healthServer{ca: ca, policyLoaded: &loaded, ping: ping}, &loaded
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	t.Run("healthy when CA valid, policy loaded, Redis reachable", func(t *testing.T) {
		t.Parallel()
		h, _ := healthFixture(t, nil)
		rec := httptest.NewRecorder()
		h.handle(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("503 when Redis unreachable", func(t *testing.T) {
		t.Parallel()
		h, _ := healthFixture(t, func(context.Context) error { return errors.New("dial refused") })
		rec := httptest.NewRecorder()
		h.handle(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "redis")
	})

	t.Run("503 when policy not loaded", func(t *testing.T) {
		t.Parallel()
		h, loaded := healthFixture(t, nil)
		loaded.Store(false)
		rec := httptest.NewRecorder()
		h.handle(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "policy")
	})

	t.Run("503 when bump CA is past rotation-due", func(t *testing.T) {
		t.Parallel()
		h, _ := healthFixture(t, nil)
		// NeedsRotation fires at 50% of validity, so now+half is always past due.
		ca, err := egressbroker.GenerateBumpCA("test", time.Now().Add(-egressbroker.CAValidity))
		require.NoError(t, err)
		h.ca = ca
		rec := httptest.NewRecorder()
		h.handle(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Contains(t, rec.Body.String(), "rotation")
	})

	t.Run("non-GET is rejected", func(t *testing.T) {
		t.Parallel()
		h, _ := healthFixture(t, nil)
		rec := httptest.NewRecorder()
		h.handle(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	})

	t.Run("health listener is loopback-only on the pinned port", func(t *testing.T) {
		t.Parallel()
		h, _ := healthFixture(t, nil)
		var loadedFlag atomic.Bool
		srv := newHealthServer(h.ca, &loadedFlag, h.ping)
		assert.Equal(t, "127.0.0.1:15083", srv.Addr)
	})
}
