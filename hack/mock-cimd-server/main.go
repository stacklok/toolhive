// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// mock-cimd-server runs a minimal OAuth AS and MCP server for manual CIMD testing.
//
// It starts two HTTP servers:
//   - :9000  mock Authorization Server that advertises client_id_metadata_document_supported
//   - :9001  mock MCP server that returns 401 pointing at the AS
//
// Usage:
//
//	go run ./hack/mock-cimd-server/
//	thv run --transport=http --url=http://localhost:9001 test-server
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	asPort  = "9000"
	mcpPort = "9001"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))

	go runAS()
	go runMCPServer()

	slog.Info("Mock servers started",
		"as", "http://localhost:"+asPort,
		"mcp", "http://localhost:"+mcpPort,
	)
	slog.Info("Run thv with:",
		"cmd", "thv run --transport=http --url=http://localhost:"+mcpPort+" test-server",
	)

	select {} // block forever
}

// runAS starts the mock Authorization Server on :9000.
func runAS() {
	mux := http.NewServeMux()

	// Discovery document — advertises CIMD support
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("Discovery request", "method", r.Method)
		issuer := "http://localhost:" + asPort
		doc := map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/oauth/authorize",
			"token_endpoint":                        issuer + "/oauth/token",
			"registration_endpoint":                 issuer + "/oauth/register",
			"jwks_uri":                              issuer + "/.well-known/jwks.json",
			"response_types_supported":              []string{"code"},
			"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
			"code_challenge_methods_supported":      []string{"S256"},
			"client_id_metadata_document_supported": true, // ← the key field
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})

	// RFC 8414 fallback
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/.well-known/openid-configuration", http.StatusMovedPermanently)
	})

	// Authorize endpoint — auto-completes immediately by redirecting back with a code.
	// It also fires the callback itself so thv run can complete without a browser.
	mux.HandleFunc("/oauth/authorize", func(w http.ResponseWriter, r *http.Request) {
		clientID := r.URL.Query().Get("client_id")
		redirectURI := r.URL.Query().Get("redirect_uri")
		state := r.URL.Query().Get("state")

		if strings.HasPrefix(clientID, "https://") {
			slog.Info("✅ CIMD client_id detected — no DCR needed", "client_id", clientID)
		} else {
			slog.Warn("⚠️  DCR-issued client_id used (CIMD was not triggered)", "client_id", clientID)
		}

		code := randomString(16)
		slog.Debug("Issuing authorization code", "code", code, "redirect_uri", redirectURI)

		callback, err := url.Parse(redirectURI)
		if err != nil || (callback.Hostname() != "localhost" && callback.Hostname() != "127.0.0.1") {
			slog.Error("Refusing to auto-complete: redirect_uri is not a localhost URL", "redirect_uri", redirectURI)
			http.Error(w, "invalid redirect_uri", http.StatusBadRequest)
			return
		}
		q := callback.Query()
		q.Set("code", code)
		q.Set("state", state)
		callback.RawQuery = q.Encode()
		callbackURL := callback.String()

		// Deliver the code directly to thv's callback server (skip browser).
		// Safe: redirect_uri is validated to be localhost above.
		go func() {
			time.Sleep(300 * time.Millisecond)
			resp, err := http.Get(callbackURL) //nolint:noctx
			if err != nil {
				slog.Error("Failed to auto-complete callback", "err", err)
				return
			}
			defer resp.Body.Close()
			slog.Info("✅ Auto-completed OAuth callback", "status", resp.StatusCode)
		}()

		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, "Authorization complete — code delivered to %s", redirectURI)
	})

	// Token endpoint — returns a minimal access token
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		slog.Info("Token exchange", "grant_type", r.FormValue("grant_type"), "client_id", r.FormValue("client_id"))
		resp := map[string]any{
			"access_token":  "mock-access-token-" + randomString(8),
			"token_type":    "Bearer",
			"expires_in":    3600,
			"refresh_token": "mock-refresh-token-" + randomString(8),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	// DCR endpoint — logs a warning if reached (CIMD should prevent this)
	mux.HandleFunc("/oauth/register", func(w http.ResponseWriter, r *http.Request) {
		slog.Warn("⚠️  DCR called — CIMD was NOT used or was rejected")
		resp := map[string]any{
			"client_id":     "dcr-fallback-" + randomString(8),
			"client_secret": "",
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Minimal JWKS
	mux.HandleFunc("/.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})

	slog.Info("Mock AS listening", "addr", ":"+asPort)
	srv := &http.Server{Addr: ":" + asPort, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("AS server error", "err", err)
		os.Exit(1)
	}
}

// runMCPServer starts a minimal MCP server on :9001 that demands OAuth.
func runMCPServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			// Return 401 with WWW-Authenticate pointing at our AS
			w.Header().Set("WWW-Authenticate",
				fmt.Sprintf(`Bearer realm="http://localhost:%s",resource_metadata="http://localhost:%s/.well-known/mcp-resource"`,
					asPort, mcpPort))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		slog.Info("✅ Authenticated MCP request received", "auth", auth[:min(len(auth), 30)]+"...")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"protocolVersion":"2025-11-05","capabilities":{}},"id":1}`))
	})

	// RFC 9728 resource metadata pointing at our AS
	mux.HandleFunc("/.well-known/mcp-resource", func(w http.ResponseWriter, _ *http.Request) {
		meta := map[string]any{
			"resource":              "http://localhost:" + mcpPort,
			"authorization_servers": []string{"http://localhost:" + asPort},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(meta)
	})

	slog.Info("Mock MCP server listening", "addr", ":"+mcpPort)
	srv := &http.Server{Addr: ":" + mcpPort, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("MCP server error", "err", err)
		os.Exit(1)
	}
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)[:n]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
