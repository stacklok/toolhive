// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHTTPReadinessProbe_ImmediateSuccess(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := serverPort(t, srv)
	probe := httpReadinessProbe(port)
	err := probe(context.Background(), nil)
	require.NoError(t, err)
}

func TestHTTPReadinessProbe_DelayedSuccess(t *testing.T) {
	t.Parallel()

	// Start a listener but don't serve initially, then start after a short delay.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	addr := ln.Addr().String()
	// Close the listener so the port is initially unresponsive.
	ln.Close()

	// Start serving after 1s.
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}
	go func() {
		time.Sleep(1 * time.Second)
		ln2, listenErr := net.Listen("tcp", addr)
		if listenErr != nil {
			return
		}
		_ = srv.Serve(ln2)
	}()
	defer srv.Close()

	probe := httpReadinessProbe(port)
	err = probe(context.Background(), nil)
	require.NoError(t, err)
}

func TestHTTPReadinessProbe_Timeout(t *testing.T) {
	t.Parallel()

	// Use a port that nothing listens on.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Use a short context timeout since we can't modify the package const.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	probe := httpReadinessProbe(port)
	err = probe(ctx, nil)
	require.Error(t, err)
}

func TestHTTPReadinessProbe_AcceptsNon200(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	port := serverPort(t, srv)
	probe := httpReadinessProbe(port)
	err := probe(context.Background(), nil)
	require.NoError(t, err)
}

func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	_, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return port
}
