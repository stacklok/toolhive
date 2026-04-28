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
