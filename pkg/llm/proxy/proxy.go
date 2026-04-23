// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package proxy implements the LLM gateway localhost reverse proxy.
package proxy

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/stacklok/toolhive/pkg/llm"
)

// TokenSource obtains fresh OIDC access tokens for the LLM gateway.
// *llm.TokenSource satisfies this interface.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Proxy is a localhost reverse proxy that strips incoming Authorization headers
// and injects fresh OIDC tokens before forwarding to the LLM gateway.
type Proxy struct {
	cfg         *llm.Config
	gatewayURL  *url.URL
	tokenSource TokenSource
	listener    net.Listener
	server      *http.Server
}

// New creates a Proxy and binds the TCP listener immediately so that Addr()
// returns the correct address before Start is called. Returns an error if
// GatewayURL is unparsable, the listen address is not loopback, or the port
// is already in use.
func New(cfg *llm.Config, ts TokenSource) (*Proxy, error) {
	gatewayURL, err := url.Parse(cfg.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid gateway URL %q: %w", cfg.GatewayURL, err)
	}
	listenAddr := fmt.Sprintf("127.0.0.1:%d", cfg.Proxy.ListenPort)
	if err := checkLoopback(listenAddr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("LLM proxy: failed to listen on %s: %w", listenAddr, err)
	}
	return &Proxy{
		cfg:         cfg,
		gatewayURL:  gatewayURL,
		tokenSource: ts,
		listener:    ln,
	}, nil
}

// Addr returns the listen address (e.g. "127.0.0.1:14000").
// Always reflects the actual bound address, even when port 0 was configured.
func (p *Proxy) Addr() string {
	return p.listener.Addr().String()
}

// checkLoopback returns an error if addr does not resolve to a loopback interface.
func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		resolved, resolveErr := net.LookupHost(host)
		if resolveErr != nil || len(resolved) == 0 {
			return fmt.Errorf("listen address %q: cannot resolve host %q", addr, host)
		}
		ip = net.ParseIP(resolved[0])
	}
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q must be a loopback interface (127.x.x.x or ::1)", addr)
	}
	return nil
}

// handler returns an http.Handler that injects a fresh OIDC token and proxies
// the request to the upstream gateway.
func (p *Proxy) handler() http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(p.gatewayURL)
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("LLM proxy upstream error", "error", err, "path", r.URL.Path)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, err := p.tokenSource.Token(r.Context())
		if err != nil {
			slog.Error("LLM proxy: failed to obtain token", "error", err)
			http.Error(w, "failed to obtain authentication token", http.StatusBadGateway)
			return
		}
		r = r.Clone(r.Context())
		r.Header.Del("Authorization")
		r.Header.Set("Authorization", "Bearer "+token)
		rp.ServeHTTP(w, r)
	})
}

// Start begins serving using the listener bound in New. It blocks until ctx is
// cancelled, then performs a graceful shutdown with a 5-second timeout.
func (p *Proxy) Start(ctx context.Context) error {
	p.server = &http.Server{
		Handler:           p.handler(),
		ReadHeaderTimeout: 30 * time.Second,
	}

	slog.Info("LLM proxy started", "addr", p.Addr(), "gateway", p.cfg.GatewayURL)

	serveErr := make(chan error, 1)
	go func() {
		if err := p.server.Serve(p.listener); err != nil && err != http.ErrServerClosed {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("LLM proxy: graceful shutdown timed out, forcing close", "error", err)
			_ = p.server.Close()
		}
		return nil
	}
}
