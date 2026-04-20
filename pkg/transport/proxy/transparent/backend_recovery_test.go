// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/transport/session"
)

// stubSessionStore is a minimal in-memory recoverySessionStore for unit tests.
type stubSessionStore struct {
	sessions map[string]session.Session
}

func newStubStore(sessions ...session.Session) *stubSessionStore {
	m := make(map[string]session.Session)
	for _, s := range sessions {
		m[s.ID()] = s
	}
	return &stubSessionStore{sessions: m}
}

func (s *stubSessionStore) Get(id string) (session.Session, bool) {
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *stubSessionStore) UpsertSession(sess session.Session) error {
	s.sessions[sess.ID()] = sess
	return nil
}

// newRecovery builds a backendRecovery backed by the given store and forward func.
func newRecovery(targetURL string, store recoverySessionStore, fwd func(*http.Request) (*http.Response, error)) *backendRecovery {
	return &backendRecovery{
		targetURI: targetURL,
		forward:   fwd,
		sessions:  store,
	}
}

// TestBackendRecoveryNoSession verifies that reinitializeAndReplay returns
// (nil, nil) when the request carries no Mcp-Session-Id.
func TestBackendRecoveryNoSession(t *testing.T) {
	t.Parallel()

	r := newRecovery("http://cluster-ip:8080", newStubStore(), nil)
	req, err := http.NewRequest(http.MethodPost, "http://cluster-ip:8080/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)

	resp, err := r.reinitializeAndReplay(req, nil)
	assert.Nil(t, resp)
	assert.NoError(t, err)
}

// TestBackendRecoveryUnknownSession verifies that reinitializeAndReplay returns
// (nil, nil) when the session ID is not in the store.
func TestBackendRecoveryUnknownSession(t *testing.T) {
	t.Parallel()

	r := newRecovery("http://cluster-ip:8080", newStubStore(), nil)
	req, err := http.NewRequest(http.MethodPost, "http://cluster-ip:8080/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", uuid.New().String())

	resp, err := r.reinitializeAndReplay(req, nil)
	assert.Nil(t, resp)
	assert.NoError(t, err)
}

// TestBackendRecoveryNoInitBody verifies that when the session has no stored
// init body, reinitializeAndReplay resets backend_url to the ClusterIP and
// returns (nil, nil) so the caller falls through to a 404 the client can handle.
func TestBackendRecoveryNoInitBody(t *testing.T) {
	t.Parallel()

	const clusterIP = "http://cluster-ip:8080"
	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	sess.SetMetadata(sessionMetadataBackendURL, "http://10.0.0.5:8080") // stale pod IP
	store := newStubStore(sess)

	r := newRecovery(clusterIP, store, nil)
	req, err := http.NewRequest(http.MethodPost, clusterIP+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := r.reinitializeAndReplay(req, nil)
	assert.Nil(t, resp)
	assert.NoError(t, err)

	// backend_url should be reset to ClusterIP so the next request routes correctly.
	updated, ok := store.Get(clientSID)
	require.True(t, ok)
	backendURL, exists := updated.GetMetadataValue(sessionMetadataBackendURL)
	require.True(t, exists)
	assert.Equal(t, clusterIP, backendURL, "backend_url should be reset to ClusterIP when no init body")
}

// TestBackendRecoveryHappyPath verifies the full re-init flow: the stored
// initialize body is replayed to the ClusterIP, the new backend session ID is
// captured, the session is updated, and the original request is replayed — all
// without standing up a full TransparentProxy.
func TestBackendRecoveryHappyPath(t *testing.T) {
	t.Parallel()

	const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize"}`
	newBackendSID := uuid.New().String()
	var (
		forwardMu    sync.Mutex
		forwardCalls []string
	)

	// Backend: returns a session ID on initialize, 200 otherwise.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		forwardMu.Lock()
		forwardCalls = append(forwardCalls, r.Header.Get("Mcp-Session-Id"))
		forwardMu.Unlock()
		if strings.Contains(string(body), `"initialize"`) {
			w.Header().Set("Mcp-Session-Id", newBackendSID)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	sess.SetMetadata(sessionMetadataInitBody, initBody)
	store := newStubStore(sess)

	r := newRecovery(backend.URL, store, http.DefaultTransport.RoundTrip)

	origBody := []byte(`{"method":"tools/list"}`)
	req, err := http.NewRequest(http.MethodPost, backend.URL+"/mcp",
		bytes.NewReader(origBody))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", clientSID)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.reinitializeAndReplay(req, origBody)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Verify session was updated with new backend SID and a pod URL.
	updated, ok := store.Get(clientSID)
	require.True(t, ok)
	backendSID, exists := updated.GetMetadataValue(sessionMetadataBackendSID)
	require.True(t, exists)
	assert.Equal(t, newBackendSID, backendSID)

	backendURL, exists := updated.GetMetadataValue(sessionMetadataBackendURL)
	require.True(t, exists)
	assert.NotEmpty(t, backendURL)

	// Two forward calls: initialize + replay. The initialize must not carry
	// a session ID; the replay must carry the new backend SID.
	forwardMu.Lock()
	defer forwardMu.Unlock()
	require.Len(t, forwardCalls, 2, "forward should be called for initialize and replay")
	assert.Empty(t, forwardCalls[0], "initialize request must not carry Mcp-Session-Id")
	assert.Equal(t, newBackendSID, forwardCalls[1], "replay must carry the new backend SID")
}

// TestBackendRecoveryReinitForwardError verifies that a forward error during
// re-initialization is returned to the caller.
func TestBackendRecoveryReinitForwardError(t *testing.T) {
	t.Parallel()

	// Server that is immediately closed — all connections will be refused.
	dead := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	sess.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	store := newStubStore(sess)

	r := newRecovery(deadURL, store, http.DefaultTransport.RoundTrip)

	req, err := http.NewRequest(http.MethodPost, deadURL+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := r.reinitializeAndReplay(req, []byte(`{"method":"tools/list"}`))
	assert.Nil(t, resp)
	assert.Error(t, err, "forward error during re-init should be returned")
}

// TestBackendRecoveryNoNewSessionID verifies that when the re-initialize
// response carries no Mcp-Session-Id, reinitializeAndReplay resets backend_url
// to ClusterIP and returns (nil, nil).
func TestBackendRecoveryNoNewSessionID(t *testing.T) {
	t.Parallel()

	// Backend that returns no Mcp-Session-Id on initialize.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // no Mcp-Session-Id header
	}))
	defer backend.Close()

	clientSID := uuid.New().String()
	sess := session.NewProxySession(clientSID)
	sess.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	sess.SetMetadata(sessionMetadataBackendURL, "http://10.0.0.5:8080")
	store := newStubStore(sess)

	// targetURI points to backend (so the init request succeeds), but we verify
	// that backend_url is reset to targetURI when no session ID comes back.
	r := newRecovery(backend.URL, store, http.DefaultTransport.RoundTrip)

	req, err := http.NewRequest(http.MethodPost, backend.URL+"/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := r.reinitializeAndReplay(req, []byte(`{"method":"tools/list"}`))
	assert.Nil(t, resp)
	assert.NoError(t, err)

	updated, ok := store.Get(clientSID)
	require.True(t, ok)
	backendURL, exists := updated.GetMetadataValue(sessionMetadataBackendURL)
	require.True(t, exists)
	assert.Equal(t, backend.URL, backendURL, "backend_url should fall back to targetURI when no new session ID")
}

// TestPodBackendURLWithCapturedAddr verifies that a captured pod IP replaces the
// host in targetURI while preserving the scheme.
func TestPodBackendURLWithCapturedAddr(t *testing.T) {
	t.Parallel()

	r := &backendRecovery{targetURI: "http://cluster-ip:8080"}
	got := r.podBackendURL("10.0.0.5:8080")
	assert.Equal(t, "http://10.0.0.5:8080", got)
}

// TestPodBackendURLFallback verifies that an empty captured address falls back
// to targetURI unchanged.
func TestPodBackendURLFallback(t *testing.T) {
	t.Parallel()

	r := &backendRecovery{targetURI: "http://cluster-ip:8080"}
	got := r.podBackendURL("")
	assert.Equal(t, "http://cluster-ip:8080", got)
}

// TestPodBackendURLHTTPSFallback verifies that an HTTPS targetURI is never
// rewritten to a pod IP. IP-literal HTTPS URLs fail TLS verification because
// server certificates are issued for hostnames, not pod IPs.
func TestPodBackendURLHTTPSFallback(t *testing.T) {
	t.Parallel()

	r := &backendRecovery{targetURI: "https://mcp.example.com/mcp"}
	got := r.podBackendURL("1.2.3.4:443")
	assert.Equal(t, "https://mcp.example.com/mcp", got,
		"HTTPS target must not be rewritten to a pod IP")
}
