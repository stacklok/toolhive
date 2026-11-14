// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver_test

import (
	"context"
	"net"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	authserverrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/test/integration/authserver/helpers"
)

// startEmbeddedAuthServer starts an embedded auth server on a loopback listener
// with a mock upstream. The issuer is derived from the listener address, so
// mutate receives it to wire clients, audiences and redirect URIs. The returned
// issuer is that listener address, not a cfg.Issuer that mutate may have
// overridden. A nil stor argument lets the server choose its storage from
// cfg.Storage. The returned stop func is idempotent and also registered with
// t.Cleanup, so a test can stop the server mid-way to model a restart.
func startEmbeddedAuthServer(
	t *testing.T,
	mutate func(cfg *authserver.RunConfig, issuer string),
	stor storage.Storage,
	opts ...authserverrunner.EmbeddedAuthServerOption,
) (server *httptest.Server, issuer string, embedded *authserverrunner.EmbeddedAuthServer, stop func()) {
	t.Helper()

	upstream := helpers.NewMockUpstreamIDP(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	issuer = "http://" + listener.Addr().String()

	cfg := helpers.NewTestAuthServerConfig(t, upstream.URL())
	cfg.Issuer = issuer
	cfg.Upstreams[0].OAuth2Config.RedirectURI = issuer + "/oauth/callback"
	cfg.InsecureAllowConfidentialOverLoopbackHTTP = true
	if mutate != nil {
		mutate(cfg, issuer)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	if stor != nil {
		embedded, err = authserverrunner.NewEmbeddedAuthServerWithStorage(ctx, cfg, stor, opts...)
	} else {
		embedded, err = authserverrunner.NewEmbeddedAuthServer(ctx, cfg, opts...)
	}
	if err != nil {
		_ = listener.Close()
		require.NoError(t, err)
	}

	server = httptest.NewUnstartedServer(embedded.Handler())
	server.Listener = listener
	server.Start()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			server.Close()
			assert.NoError(t, embedded.Close())
		})
	}
	t.Cleanup(stop)
	return server, issuer, embedded, stop
}
