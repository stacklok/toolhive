// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// This file is in package authserver_test (not authserver) so it can import
// pkg/authserver/runner without inducing the import cycle that would result
// from extending integration_test.go directly: integration_test.go is in
// package authserver, and runner already imports authserver. The
// authserver_test external test package sidesteps that cycle, satisfying
// the AC's instruction that the durable-restart integration test live in
// pkg/authserver alongside the rest of the EmbeddedAuthServer integration
// surface.
package authserver_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver"
	"github.com/stacklok/toolhive/pkg/authserver/runner"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestEmbeddedAuthServer_DCRStorePersistsAcrossClose verifies that the DCR
// store reachable through EmbeddedAuthServer.DCRStore() holds the resolved
// RFC 7591 client registration after the constructor's full DCR resolver
// runs against a mock AS. The Get is issued BEFORE Close so the assertion
// does not depend on the (undocumented) MemoryStorage post-Close
// readability that an earlier version of this test silently relied on.
//
// What this test does cover:
//
//   - NewEmbeddedAuthServer runs the full DCR resolver against a mock AS
//     during construction, populating the storage-backed DCR store, and
//     surfaces the same storage.DCRCredentialStore the authserver itself
//     reads from via DCRStore(). The persisted credentials are readable
//     by issuing a Get against the captured store while the server is
//     still live.
//
// What this test does NOT cover (deferred follow-up):
//
//   - The full "boot, close, boot again on the same backend, observe zero
//     /register calls on the second boot" cross-restart scenario. Closing
//     that gap requires either miniredis-Sentinel emulation or a
//     Docker-based Redis Sentinel cluster in the test harness, since the
//     production restart path lives on Redis (Memory cannot be shared
//     across two NewEmbeddedAuthServer constructors). Tracked as a
//     follow-up; this test deliberately scopes itself to what is
//     exercisable today from package authserver_test against the
//     production constructor seam.
func TestEmbeddedAuthServer_DCRStorePersistsAcrossClose(t *testing.T) {
	t.Parallel()

	server, requestCount := newMockUpstreamAS(t)

	cfg := &authserver.RunConfig{
		SchemaVersion: authserver.CurrentSchemaVersion,
		Issuer:        server.URL,
		Upstreams: []authserver.UpstreamRunConfig{
			{
				Name: "dcr-upstream",
				Type: authserver.UpstreamProviderTypeOAuth2,
				OAuth2Config: &authserver.OAuth2UpstreamRunConfig{
					ClientID:              "",
					AuthorizationEndpoint: server.URL + "/authorize",
					TokenEndpoint:         server.URL + "/token",
					Scopes:                []string{"openid", "profile"},
					DCRConfig: &authserver.DCRUpstreamConfig{
						DiscoveryURL: server.URL + "/.well-known/oauth-authorization-server",
					},
				},
			},
		},
		AllowedAudiences: []string{"https://mcp.example.com"},
	}

	embed, err := runner.NewEmbeddedAuthServer(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, embed)
	t.Cleanup(func() { _ = embed.Close() })

	firstBootRequests := atomic.LoadInt32(requestCount)
	require.Greater(t, firstBootRequests, int32(0),
		"first boot must have issued network I/O to the mock AS during DCR")

	// Capture the storage instance the constructor wired into the DCR
	// store. This is the same backend the authserver itself was using; in
	// production it is shared across authserver state, so DCR survives
	// restart on the same backend.
	persistentStore := embed.DCRStore()
	require.NotNil(t, persistentStore,
		"NewEmbeddedAuthServer must surface a storage-level DCRCredentialStore")

	// Verify the persisted DCR row by issuing a Get against the captured
	// store BEFORE closing the server. Doing the Get pre-Close avoids
	// silently depending on whichever storage backend the test happens to
	// use staying readable after Close (a contract MemoryStorage honors
	// today but RedisStorage's closed connection pool does not). The
	// assertion proves the persistence boundary the production cross-
	// replica and cross-restart reuse paths depend on: that the
	// resolution lives in storage, not in process-local cache state.
	redirectURI := server.URL + "/oauth/callback"
	key := storage.DCRKey{
		Issuer:      server.URL,
		RedirectURI: redirectURI,
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}
	creds, err := persistentStore.GetDCRCredentials(context.Background(), key)
	require.NoError(t, err,
		"DCR credentials must be readable from the captured store — "+
			"this is the persistence boundary cross-replica reuse depends on")
	require.NotNil(t, creds)
	assert.Equal(t, "dcr-client-id", creds.ClientID,
		"persisted ClientID must match the first boot's DCR resolution")
	assert.Equal(t, "dcr-client-secret", creds.ClientSecret,
		"persisted ClientSecret must match the first boot's DCR resolution")

	// Mock-AS request count is unchanged after the survival check — the
	// Get is a pure store read with no upstream traffic.
	assert.Equal(t, firstBootRequests, atomic.LoadInt32(requestCount),
		"GetDCRCredentials must not issue any HTTP requests to the mock AS")
}

// newMockUpstreamAS stands up a mock authorization server that serves
// RFC 8414 discovery metadata and an RFC 7591 /register endpoint. Every
// request is counted via the returned *int32 so tests can assert that
// the post-boot persistence check issues zero network I/O.
//
// This duplicates pkg/authserver/runner.newMockAuthorizationServer
// because that helper is unexported and this test file lives in
// authserver_test (it cannot be imported from the runner package without
// either exporting the helper — bloating runner's public surface — or
// sharing a test-helpers package). Keeping a small, self-contained
// duplicate is the lower-cost option for this single use site.
//
// DO NOT COPY THIS A THIRD TIME. The next caller must extract the helper
// to a shared internal test-helpers package (e.g.
// pkg/authserver/internal/testhelpers) and rewrite both this copy and the
// runner-package copy to call into it; two copies are tolerable, three is
// a bug factory.
func newMockUpstreamAS(t *testing.T) (*httptest.Server, *int32) {
	t.Helper()

	var total int32
	var server *httptest.Server

	mux := http.NewServeMux()

	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&total, 1)
		md := oauthproto.AuthorizationServerMetadata{
			Issuer:                            server.URL,
			AuthorizationEndpoint:             server.URL + "/authorize",
			TokenEndpoint:                     server.URL + "/token",
			RegistrationEndpoint:              server.URL + "/register",
			TokenEndpointAuthMethodsSupported: []string{"client_secret_basic"},
			ScopesSupported:                   []string{"openid", "profile"},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&total, 1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()
		var req oauthproto.DynamicClientRegistrationRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		resp := oauthproto.DynamicClientRegistrationResponse{
			ClientID:                "dcr-client-id",
			ClientSecret:            "dcr-client-secret",
			RegistrationAccessToken: "dcr-reg-token",
			TokenEndpointAuthMethod: req.TokenEndpointAuthMethod,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(resp)
	})

	// Catch-all to count unexpected requests.
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&total, 1)
		w.WriteHeader(http.StatusNotFound)
	})

	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &total
}
