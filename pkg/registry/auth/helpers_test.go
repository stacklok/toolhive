// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newOIDCTestServer starts an httptest server that handles the well-known OIDC
// discovery endpoints used by CreateOAuthConfigFromOIDC. It shuts down
// automatically when the test completes.
func newOIDCTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	handler := func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/jwks",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}
	mux.HandleFunc("/.well-known/openid-configuration", handler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", handler)

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newTokenServer builds a server that handles OIDC discovery AND a token
// endpoint that returns the given access token and refresh token.
func newTokenServer(t *testing.T, at, rt string) *httptest.Server {
	t.Helper()

	var srv *httptest.Server
	mux := http.NewServeMux()

	oidcHandler := func(w http.ResponseWriter, _ *http.Request) {
		doc := map[string]string{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}
	mux.HandleFunc("/.well-known/openid-configuration", oidcHandler)
	mux.HandleFunc("/.well-known/oauth-authorization-server", oidcHandler)
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"` + at + `","refresh_token":"` + rt + `","token_type":"Bearer","expires_in":3600}`))
	})

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
