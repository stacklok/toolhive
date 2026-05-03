// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

// testHelper is a minimal subset of testing.TB and ginkgo.GinkgoTInterface that
// the CIMD mock server helpers require. Both *testing.T and GinkgoT() satisfy
// this interface, so helpers can be called from plain Go tests and Ginkgo specs.
type testHelper interface {
	Helper()
	Cleanup(func())
}

// cimdAuthRequest captures parameters from an OAuth authorization request.
type cimdAuthRequest struct {
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
}

// cimdMockAuthServer is a minimal httptest-based mock authorization server
// for CIMD testing. Unlike OIDCMockServer (Fosite-backed), this server accepts
// any HTTPS URL as a client_id, which is required to verify CIMD behaviour.
type cimdMockAuthServer struct {
	server          *httptest.Server
	authRequestChan chan cimdAuthRequest

	mu               sync.Mutex
	lastClientID     string
	dcrCalled        bool
	cimdSupported    bool
	rejectCIMD       bool
	cimdRejectedOnce bool
}

// newCIMDMockAuthServer creates and starts a mock authorization server that
// advertises client_id_metadata_document_supported. It registers t.Cleanup to
// close the server automatically. Pass rejectCIMD=true to make the server
// reject the first authorization request that uses a CIMD client_id (an HTTPS
// URL), simulating an AS that advertises CIMD support but rejects it at
// runtime, triggering the DCR fallback path in ToolHive.
func newCIMDMockAuthServer(tb testHelper, cimdSupported bool, rejectCIMD bool) *cimdMockAuthServer {
	tb.Helper()

	s := &cimdMockAuthServer{
		authRequestChan: make(chan cimdAuthRequest, 4),
		cimdSupported:   cimdSupported,
		rejectCIMD:      rejectCIMD,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", s.handleDiscovery)
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/token", s.handleToken)
	mux.HandleFunc("/oauth/register", s.handleRegister)
	mux.HandleFunc("/.well-known/jwks.json", s.handleJWKS)
	mux.HandleFunc("/.well-known/mcp-resource", s.handleResourceMetadata)

	s.server = httptest.NewServer(mux)
	tb.Cleanup(s.server.Close)

	return s
}

// URL returns the base URL of the mock authorization server.
func (s *cimdMockAuthServer) URL() string {
	return s.server.URL
}

// IssuerURL returns the issuer URL (same as URL for this mock).
func (s *cimdMockAuthServer) IssuerURL() string {
	return s.server.URL
}

// ResourceMetadataURL returns the RFC 9728 resource metadata URL for this server.
func (s *cimdMockAuthServer) ResourceMetadataURL() string {
	return fmt.Sprintf("%s/.well-known/mcp-resource", s.server.URL)
}

// WaitForAuthRequest blocks until an authorization request arrives or the timeout
// elapses.
func (s *cimdMockAuthServer) WaitForAuthRequest(timeout time.Duration) (cimdAuthRequest, error) {
	select {
	case req := <-s.authRequestChan:
		return req, nil
	case <-time.After(timeout):
		return cimdAuthRequest{}, fmt.Errorf("timeout waiting for auth request after %s", timeout)
	}
}

// DcrWasCalled returns true if the DCR /oauth/register endpoint was ever called.
func (s *cimdMockAuthServer) DcrWasCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dcrCalled
}

// LastClientID returns the most recent client_id seen in /oauth/authorize.
func (s *cimdMockAuthServer) LastClientID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastClientID
}

// handleDiscovery serves the OIDC discovery document. It sets
// client_id_metadata_document_supported based on the server's configuration.
func (s *cimdMockAuthServer) handleDiscovery(w http.ResponseWriter, _ *http.Request) {
	doc := map[string]interface{}{
		"issuer":                                s.server.URL,
		"authorization_endpoint":                fmt.Sprintf("%s/oauth/authorize", s.server.URL),
		"token_endpoint":                        fmt.Sprintf("%s/oauth/token", s.server.URL),
		"registration_endpoint":                 fmt.Sprintf("%s/oauth/register", s.server.URL),
		"jwks_uri":                              fmt.Sprintf("%s/.well-known/jwks.json", s.server.URL),
		"code_challenge_methods_supported":      []string{"S256"},
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"client_id_metadata_document_supported": s.cimdSupported,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// RejectCIMDWasCalled returns true if the server rejected a CIMD client_id at
// least once. Callers use this to assert that the CIMD path was attempted
// before the DCR fallback fired.
func (s *cimdMockAuthServer) RejectCIMDWasCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cimdRejectedOnce
}

// handleAuthorize captures the authorization request and either immediately
// redirects (when auto_complete=true) or places the request into the channel
// for the test to inspect.
//
// When rejectCIMD is true, the first request whose client_id is an HTTPS URL
// (i.e. a CIMD metadata document URL) is rejected by redirecting to the
// callback with error=invalid_client. This simulates an AS that advertises
// CIMD support but rejects it at the authorization endpoint, triggering the
// DCR fallback path in ToolHive.
func (s *cimdMockAuthServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := cimdAuthRequest{
		ClientID:      q.Get("client_id"),
		RedirectURI:   q.Get("redirect_uri"),
		State:         q.Get("state"),
		CodeChallenge: q.Get("code_challenge"),
	}

	s.mu.Lock()
	s.lastClientID = req.ClientID

	// If rejectCIMD is armed and this is the first CIMD request, reject it.
	// A CIMD client_id is any HTTPS URL (see oauthproto.IsClientIDMetadataDocumentURL).
	if s.rejectCIMD && !s.cimdRejectedOnce && isCIMDClientID(req.ClientID) {
		s.cimdRejectedOnce = true
		s.mu.Unlock()

		redirectURI := req.RedirectURI
		if redirectURI == "" {
			http.Error(w, "missing redirect_uri", http.StatusBadRequest)
			return
		}
		separator := "?"
		for _, ch := range redirectURI {
			if ch == '?' {
				separator = "&"
				break
			}
		}
		http.Redirect(w, r,
			fmt.Sprintf("%s%serror=invalid_client&state=%s&error_description=cimd+not+supported",
				redirectURI, separator, req.State),
			http.StatusFound,
		)
		return
	}
	s.mu.Unlock()

	// Always send into the channel so WaitForAuthRequest can inspect it.
	select {
	case s.authRequestChan <- req:
	default:
		// Channel buffer full; drop the duplicate.
	}

	if q.Get("auto_complete") == "true" {
		redirectURI := req.RedirectURI
		if redirectURI == "" {
			http.Error(w, "missing redirect_uri", http.StatusBadRequest)
			return
		}
		separator := "&"
		if len(q.Get("redirect_uri")) > 0 {
			// redirect_uri itself may or may not have a query string already;
			// we append to it by adding a '?' if needed.
			separator = "?"
			for _, ch := range redirectURI {
				if ch == '?' {
					separator = "&"
					break
				}
			}
		}
		http.Redirect(w, r,
			fmt.Sprintf("%s%scode=test-auth-code&state=%s", redirectURI, separator, req.State),
			http.StatusFound,
		)
		return
	}

	// Without auto_complete the test must drive the flow externally.
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("authorization pending"))
}

// handleToken accepts any code=test-auth-code and returns a minimal access token.
func (*cimdMockAuthServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	tokenResp := map[string]interface{}{
		"access_token":  "test-access-token-cimd",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": "test-refresh-token-cimd",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tokenResp)
}

// handleRegister is the DCR endpoint. Calling it records that DCR was used.
func (s *cimdMockAuthServer) handleRegister(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	s.dcrCalled = true
	s.mu.Unlock()

	resp := map[string]interface{}{
		"client_id":     "dcr-issued-client-id",
		"client_secret": "dcr-issued-secret",
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

// handleJWKS returns an empty JWKS set.
func (*cimdMockAuthServer) handleJWKS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"keys":[]}`))
}

// handleResourceMetadata returns RFC 9728 protected resource metadata pointing
// at this authorization server.
func (s *cimdMockAuthServer) handleResourceMetadata(w http.ResponseWriter, _ *http.Request) {
	meta := map[string]interface{}{
		"resource":              s.server.URL,
		"authorization_servers": []string{s.server.URL},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meta)
}

// isCIMDClientID returns true if clientID looks like a CIMD metadata document
// URL (i.e. any HTTPS URL). This mirrors oauthproto.IsClientIDMetadataDocumentURL
// without importing the production package from a test helper.
func isCIMDClientID(clientID string) bool {
	return len(clientID) >= 8 && clientID[:8] == "https://"
}

// newCIMDMockMCPServer creates a minimal httptest MCP server that:
//   - Returns 401 with WWW-Authenticate header when there is no Authorization header.
//   - Returns a minimal JSON-RPC success response when an Authorization header is present.
//
// asURL is the base URL of the authorization server; it is embedded in the
// WWW-Authenticate header's realm and resource_metadata attributes.
func newCIMDMockMCPServer(tb testHelper, asURL string) *httptest.Server {
	tb.Helper()

	resourceMetaURL := fmt.Sprintf("%s/.well-known/mcp-resource", asURL)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set(
				"WWW-Authenticate",
				fmt.Sprintf(`Bearer realm="%s",resource_metadata="%s"`, asURL, resourceMetaURL),
			)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Minimal JSON-RPC success response so the proxy can verify connectivity.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"cimd-mock-mcp","version":"0.0.1"}}}`))
	}))

	tb.Cleanup(srv.Close)
	return srv
}
