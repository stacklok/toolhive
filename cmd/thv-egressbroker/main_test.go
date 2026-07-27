// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/egressbroker"
	"github.com/stacklok/toolhive/pkg/vmcp/session/untrusted"
)

// TestBrokerPortContract pins the clone-time ↔ broker port contract: the
// health listener binds the port the operator-rendered probes target
// (untrusted.BrokerHealthPort), and the Envoy explicit-proxy port the
// backend's HTTP(S)_PROXY env points at equals the broker-rendered bootstrap
// default.
func TestBrokerPortContract(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 15083, untrusted.BrokerHealthPort,
		"the health listener port must match the clone-time probe wiring")
	assert.Equal(t, 15001, egressbroker.DefaultProxyPort,
		"the Envoy proxy port must match the backend HTTP(S)_PROXY env the clone wiring renders")
}

// TestHealthListenerBind performs a real bind on the pinned health port. The
// listener must bind all interfaces (the pod IP): kubelet httpGet probes are
// delivered to the pod IP from the node network, so a loopback-only bind makes
// every probe fail and the container is killed at the liveness threshold
// before it can initialize. The bind is the assertion; port conflicts skip
// rather than fail (another listener on 15083 says nothing about the bind).
func TestHealthListenerBind(t *testing.T) {
	t.Parallel()

	h, _ := healthFixture(t, nil)
	var loadedFlag atomic.Bool
	loadedFlag.Store(true)
	srv := newHealthServerHandler(&healthServer{ca: h.ca, policyLoaded: &loadedFlag, ping: h.ping})

	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		t.Skipf("health port %s already in use; cannot assert the bind", srv.Addr)
	}
	defer ln.Close()

	// The bound address must be the wildcard (all interfaces) so kubelet
	// probes delivered to the pod IP reach it.
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	assert.Equal(t, "15083", port)

	// Serve one real request through the bound listener.
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	resp, err := http.Get(fmt.Sprintf("http://%s/healthz", ln.Addr().String()))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

//nolint:paralleltest // t.Setenv modifies the process environment; subtests cannot run in parallel.
func TestTokenEncOption(t *testing.T) {
	kek32 := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	kekShort := base64.StdEncoding.EncodeToString([]byte("too-short"))

	t.Run("no KEK env at all means plaintext legacy rows (nil options)", func(t *testing.T) {
		opts, err := tokenEncOption()
		require.NoError(t, err)
		assert.Nil(t, opts)
	})

	t.Run("active ID without key entries is startup-fatal", func(t *testing.T) {
		t.Setenv(envKEKActiveID, "kek-1")
		_, err := tokenEncOption()
		require.Error(t, err)
		assert.Contains(t, err.Error(), envKEKActiveID)
	})

	t.Run("key entries without an active ID are startup-fatal", func(t *testing.T) {
		t.Setenv(envKEKPrefix+"kek-1", kek32)
		_, err := tokenEncOption()
		require.Error(t, err, "an empty active key ID must fail the keyring constructor")
	})

	t.Run("active ID absent from the key set is startup-fatal", func(t *testing.T) {
		t.Setenv(envKEKActiveID, "kek-9")
		t.Setenv(envKEKPrefix+"kek-1", kek32)
		_, err := tokenEncOption()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "kek-9")
	})

	t.Run("non-base64 key is startup-fatal", func(t *testing.T) {
		t.Setenv(envKEKActiveID, "kek-1")
		t.Setenv(envKEKPrefix+"kek-1", "!!!not-base64!!!")
		_, err := tokenEncOption()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "base64")
	})

	t.Run("empty key value is startup-fatal", func(t *testing.T) {
		t.Setenv(envKEKActiveID, "kek-1")
		t.Setenv(envKEKPrefix+"kek-1", "  ")
		_, err := tokenEncOption()
		require.Error(t, err)
	})

	t.Run("wrong-length key is startup-fatal", func(t *testing.T) {
		t.Setenv(envKEKActiveID, "kek-1")
		t.Setenv(envKEKPrefix+"kek-1", kekShort)
		_, err := tokenEncOption()
		require.Error(t, err)
	})

	t.Run("KEK_ID alone is not mistaken for a key entry", func(t *testing.T) {
		// Only THV_EGRESSBROKER_KEK_ID set: the active-ID coordinate must not
		// be parsed as a per-ID key named "ID" (that would make the "active
		// ID without keys" error branch unreachable).
		t.Setenv(envKEKActiveID, "kek-1")
		_, err := tokenEncOption()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no "+envKEKPrefix+"<key-id> key entries")
	})

	t.Run("multi-key set (active + retired) builds the keyring", func(t *testing.T) {
		t.Setenv(envKEKActiveID, "kek-2")
		t.Setenv(envKEKPrefix+"kek-1", kek32)
		t.Setenv(envKEKPrefix+"kek-2", kek32)
		opts, err := tokenEncOption()
		require.NoError(t, err)
		require.Len(t, opts, 1)
	})
}
