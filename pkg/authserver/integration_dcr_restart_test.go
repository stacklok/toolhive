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

// TestEmbeddedAuthServer_DCRSurvivesRestart is the authserver-restart analog
// of pkg/authserver/runner.TestBuildUpstreamConfigs_DCR's cache-hit
// assertion, crossing an EmbeddedAuthServer lifecycle boundary instead of
// just two buildUpstreamConfigs calls.
//
// Coverage shape:
//
//   - First boot: NewEmbeddedAuthServer runs the full DCR resolver against
//     a mock AS during construction, populating the storage-backed DCR
//     store. The resolved client_id / client_secret are observable through
//     the constructor's success.
//   - Capture: the storage instance the constructor wired into the DCR
//     store is exposed via EmbeddedAuthServer.DCRStore(), which surfaces
//     the same storage.DCRCredentialStore the authserver itself reads
//     from. In production this is shared backend state; here it lets the
//     test stand in for "what the next boot would see if it were pointed
//     at the same backend."
//   - Teardown: the first server is closed, releasing handlers and
//     backend resources. The persisted DCR row remains in the captured
//     storage.DCRCredentialStore.
//   - Second resolve: a second NewEmbeddedAuthServer with the same
//     upstream config and a fresh (memory) storage would freshly register
//     against the mock AS, since memory storage cannot be shared across
//     two NewEmbeddedAuthServer constructors today (createStorage always
//     produces a new MemoryStorage). The "shared backend" case — where
//     the second constructor reads the first boot's DCR row — is the
//     production Redis path, which this test cannot exercise without a
//     real Sentinel cluster (miniredis does not speak the Sentinel
//     protocol). Instead, this test verifies the persistence boundary by
//     issuing a Get directly against the captured store and asserting
//     the row survived the first server's Close. That is the strongest
//     coverage achievable from package authserver_test against the
//     current production constructor seam.
//
// Documented gap: the full "boot, close, boot again, observe zero
// /register calls on the second boot" scenario is not exercised in this
// test (or anywhere else in the repo). Closing it requires either
// miniredis-Sentinel emulation or a Docker-based Redis Sentinel cluster
// in the test harness; both are deferred to a follow-up. The wiring
// that the second boot would consume — the type of dcrStore being the
// same storage.DCRCredentialStore that authserver.New writes through —
// is verified at compile time by the storage.Storage interface
// embedding storage.DCRCredentialStore and by the existing
// TestNewEmbeddedAuthServer_DCRBoot in pkg/authserver/runner.
func TestEmbeddedAuthServer_DCRSurvivesRestart(t *testing.T) {
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

	firstBootRequests := atomic.LoadInt32(requestCount)
	require.Greater(t, firstBootRequests, int32(0),
		"first boot must have issued network I/O to the mock AS during DCR")

	// Capture the storage instance the constructor wired into the DCR
	// store before tearing down the server. This is the same backend the
	// authserver itself was using; in production it is shared across
	// authserver state, so DCR survives restart on the same backend.
	persistentStore := embed.DCRStore()
	require.NotNil(t, persistentStore,
		"NewEmbeddedAuthServer must surface a storage-level DCRCredentialStore")

	// Tear down the first server. Its DCR registration is now persisted in
	// persistentStore.
	require.NoError(t, embed.Close())

	// Verify the DCR row survived the first server's Close — the
	// persistence boundary that production cross-replica / cross-restart
	// reuse depends on. The exact cache key construction is the runner
	// resolver's responsibility (issuer + redirect URI + scopes hash);
	// here we assemble the same key shape using the public canonical
	// helpers so the assertion does not silently drift if the resolver
	// changes how keys are computed.
	redirectURI := server.URL + "/oauth/callback"
	key := storage.DCRKey{
		Issuer:      server.URL,
		RedirectURI: redirectURI,
		ScopesHash:  storage.ScopesHash([]string{"openid", "profile"}),
	}
	creds, err := persistentStore.GetDCRCredentials(context.Background(), key)
	require.NoError(t, err,
		"DCR credentials must remain readable from the captured store after Close — "+
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
