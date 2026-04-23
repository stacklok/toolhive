// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package proxy implements the LLM gateway localhost reverse proxy.
package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/stacklok/toolhive/pkg/llm"
	"github.com/stacklok/toolhive/pkg/networking"
)

// TokenSource obtains fresh OIDC access tokens for the LLM gateway.
// *llm.TokenSource satisfies this interface.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// tokenContextKey is the context key used to pass the injected token to the
// Rewrite hook without modifying the original incoming request.
type tokenContextKey struct{}

// proxyTransport is an http.RoundTripper that delegates to Proxy.transport,
// falling back to http.DefaultTransport. Using a pointer to Proxy allows tests
// to set p.transport after New() without rebuilding the ReverseProxy.
type proxyTransport Proxy

func (pt *proxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if pt.transport != nil {
		return pt.transport.RoundTrip(req)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// Proxy is a localhost reverse proxy that strips incoming Authorization headers
// and injects fresh OIDC tokens before forwarding to the LLM gateway.
type Proxy struct {
	cfg         *llm.Config
	gatewayURL  *url.URL
	tokenSource TokenSource
	listener    net.Listener
	server      *http.Server
	rp          *httputil.ReverseProxy
	// transport overrides the HTTP transport used to reach the upstream gateway.
	// nil means http.DefaultTransport. Set in tests to trust self-signed TLS certs.
	transport http.RoundTripper
}

// New creates a Proxy and binds the TCP listener immediately so that Addr()
// returns the correct address before Start is called. Returns an error if
// GatewayURL is unparsable, the listen address is not loopback, or the port
// is already in use.
func New(cfg *llm.Config, ts TokenSource) (*Proxy, error) {
	if cfg == nil {
		return nil, fmt.Errorf("cfg must not be nil")
	}
	if ts == nil {
		return nil, fmt.Errorf("ts must not be nil")
	}
	gatewayURL, err := url.Parse(cfg.GatewayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid gateway URL %q: %w", cfg.GatewayURL, err)
	}
	if gatewayURL.Host == "" {
		return nil, fmt.Errorf("invalid gateway URL %q: must have scheme and host", cfg.GatewayURL)
	}
	if gatewayURL.Scheme != "https" {
		return nil, fmt.Errorf("gateway URL must use HTTPS, got scheme %q", gatewayURL.Scheme)
	}
	listenAddr := fmt.Sprintf("127.0.0.1:%d", cfg.EffectiveProxyPort())
	if err := networking.ValidateLoopbackAddress(listenAddr); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("LLM proxy: failed to listen on %s: %w", listenAddr, err)
	}

	p := &Proxy{
		cfg:         cfg,
		gatewayURL:  gatewayURL,
		tokenSource: ts,
		listener:    ln,
	}
	p.rp = &httputil.ReverseProxy{
		// Transport delegates to p.transport so tests can swap it after New().
		Transport: (*proxyTransport)(p),
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(p.gatewayURL)
			// Always strip the client-supplied Authorization header first so it
			// is never forwarded upstream, even if the injected token is empty.
			pr.Out.Header.Del("Authorization")
			if tok, _ := pr.Out.Context().Value(tokenContextKey{}).(string); tok != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+tok)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Error("LLM proxy upstream error", "error", err, "path", r.URL.Path)
			http.Error(w, "upstream error", http.StatusBadGateway)
		},
	}
	return p, nil
}

// Addr returns the actual bound listen address (e.g. "127.0.0.1:14000").
func (p *Proxy) Addr() string {
	return p.listener.Addr().String()
}

// handler returns an http.Handler that injects a fresh OIDC token and proxies
// the request to the upstream gateway.
func (p *Proxy) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Guard against DNS-rebinding attacks: reject requests whose Host header
		// does not resolve to a loopback address. A loopback-only bind prevents
		// external TCP connections but doesn't stop browser JS from rebinding an
		// attacker-controlled hostname to 127.0.0.1 and minting OIDC tokens.
		if !networking.IsLoopbackHost(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		tokenCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		token, err := p.tokenSource.Token(tokenCtx)
		if err != nil {
			if errors.Is(err, llm.ErrTokenRequired) {
				slog.Error("LLM proxy: re-authentication required")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w,
					`{"error":{"message":"LLM gateway authentication required: `+
						`run 'thv llm setup' to log in","type":"authentication_error","code":"token_required"}}`,
				)
			} else {
				slog.Error("LLM proxy: failed to obtain token", "reason", llm.SanitizeTokenError(err))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_, _ = io.WriteString(w, `{"error":{"message":"LLM gateway token fetch failed","type":"server_error"}}`)
			}
			return
		}
		if token == "" {
			slog.Error("LLM proxy: token source returned empty token")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"message":"LLM gateway token fetch failed","type":"server_error"}}`)
			return
		}

		// Pass the token to the Rewrite hook via context so it can be set on
		// pr.Out.Header without cloning the incoming request.
		ctx := context.WithValue(r.Context(), tokenContextKey{}, token)
		p.rp.ServeHTTP(w, r.WithContext(ctx))
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
		if err := p.server.Serve(p.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
		close(serveErr)
	}()

	select {
	case err := <-serveErr:
		_ = p.server.Close()
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
