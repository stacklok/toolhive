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
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

// newRecoveryStore seeds auth-disabled sessions in the real memory store.
func newRecoveryStore(t *testing.T, sessions ...session.Session) *session.Manager {
	t.Helper()
	manager := session.NewManager(time.Hour, nil)
	t.Cleanup(func() { require.NoError(t, manager.Stop()) })
	for _, sess := range sessions {
		sess.SetMetadata(session.MetadataKeyIdentityBinding, "unauthenticated")
		require.NoError(t, manager.AddSession(sess))
	}
	return manager
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

	r := newRecovery("http://cluster-ip:8080", newRecoveryStore(t), nil)
	req, err := http.NewRequest(http.MethodPost, "http://cluster-ip:8080/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)

	resp, err := r.reinitializeAndReplay(req, nil)
	assert.Nil(t, resp)
	assert.NoError(t, err)
}

// TestBackendRecoveryUnknownSession verifies that unknown sessions fail closed
// before backend recovery performs any work.
func TestBackendRecoveryUnknownSession(t *testing.T) {
	t.Parallel()

	r := newRecovery("http://cluster-ip:8080", newRecoveryStore(t), nil)
	req, err := http.NewRequest(http.MethodPost, "http://cluster-ip:8080/mcp",
		strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", uuid.New().String())

	resp, err := r.reinitializeAndReplay(req, nil)
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
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
	store := newRecoveryStore(t, sess)

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

func TestBackendRecoveryConditionalUpsertPreservesReplacement(t *testing.T) {
	t.Parallel()

	const clusterIP = "http://cluster-ip:8080"
	id := uuid.NewString()
	replacement := session.NewProxySession(id)
	replacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00replacement")
	replacement.SetMetadata("version", "replacement")
	storage := &replacingStorage{
		Storage:          session.NewLocalStorage(),
		replacement:      replacement,
		replaceAfterLoad: true,
	}
	store := session.NewManagerWithStorage(time.Hour, func(id string) session.Session {
		return session.NewProxySession(id)
	}, storage)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })
	original := session.NewProxySession(id)
	original.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	original.SetMetadata(sessionMetadataBackendURL, "http://10.0.0.5:8080")
	require.NoError(t, store.AddSession(original))
	r := newRecovery(clusterIP, store, nil)
	req, err := http.NewRequest(http.MethodPost, clusterIP+"/mcp", strings.NewReader(`{"method":"tools/list"}`))
	require.NoError(t, err)
	req.Header.Set("Mcp-Session-Id", id)

	resp, err := r.reinitializeAndReplay(req, nil)

	require.NoError(t, err)
	require.NotNil(t, resp)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.False(t, storage.replaceAfterLoad)
	metadata, err := storage.Storage.LoadMetadata(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00replacement", metadata[session.MetadataKeyIdentityBinding])
	require.Equal(t, "replacement", metadata["version"])
}

func TestBackendRecoveryFailedParentUpsertCleansReservation(t *testing.T) {
	t.Parallel()

	const clusterIP = "http://cluster-ip:8080"
	clientSID := uuid.NewString()
	backendSID := uuid.NewString()
	replacement := session.NewProxySession(clientSID)
	replacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00replacement")
	replacement.SetMetadata("version", "replacement")
	storage := &replacingStorage{
		Storage:          session.NewLocalStorage(),
		replacement:      replacement,
		replaceAfterLoad: true,
	}
	store := session.NewManagerWithStorage(time.Hour, func(id string) session.Session {
		return session.NewProxySession(id)
	}, storage)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })
	original := session.NewProxySession(clientSID)
	original.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	original.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, store.AddSession(original))
	var deleteCalled bool
	r := newRecovery(clusterIP, store, func(req *http.Request) (*http.Response, error) {
		deleteCalled = deleteCalled || req.Method == http.MethodDelete
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     http.Header{"Mcp-Session-Id": []string{backendSID}},
		}, nil
	})
	req := httptest.NewRequest(http.MethodPost, clusterIP+"/mcp", strings.NewReader(`{"method":"tools/list"}`))
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := r.reinitializeAndReplay(req, []byte(`{"method":"tools/list"}`))

	require.NoError(t, err)
	require.NotNil(t, resp)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.False(t, deleteCalled, "failed recovery must leave the backend session for expiry")
	_, err = storage.Storage.LoadMetadata(t.Context(), backendSID)
	require.ErrorIs(t, err, session.ErrSessionNotFound)
	metadata, err := storage.Storage.LoadMetadata(t.Context(), clientSID)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00replacement", metadata[session.MetadataKeyIdentityBinding])
	require.Equal(t, "replacement", metadata["version"])
}

func TestBackendRecoveryCleanupPreservesRacedReservation(t *testing.T) {
	t.Parallel()

	const clusterIP = "http://cluster-ip:8080"
	clientSID := uuid.NewString()
	backendSID := uuid.NewString()
	parentReplacement := session.NewProxySession(clientSID)
	parentReplacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00replacement")
	reservationReplacement := session.NewProxySession(backendSID)
	reservationReplacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00foreign")
	reservationReplacement.SetMetadata("version", "replacement")
	storage := &replacingStorage{
		Storage:               session.NewLocalStorage(),
		replacement:           parentReplacement,
		replaceAfterLoad:      true,
		replaceBeforeDeleteID: backendSID,
		deleteReplacement:     reservationReplacement,
	}
	store := session.NewManagerWithStorage(time.Hour, func(id string) session.Session {
		return session.NewProxySession(id)
	}, storage)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })
	original := session.NewProxySession(clientSID)
	original.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	original.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, store.AddSession(original))
	var deleteCalled bool
	r := newRecovery(clusterIP, store, func(req *http.Request) (*http.Response, error) {
		deleteCalled = deleteCalled || req.Method == http.MethodDelete
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     http.Header{"Mcp-Session-Id": []string{backendSID}},
		}, nil
	})
	req := httptest.NewRequest(http.MethodPost, clusterIP+"/mcp", strings.NewReader(`{"method":"tools/list"}`))
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := r.reinitializeAndReplay(req, []byte(`{"method":"tools/list"}`))

	require.NoError(t, err)
	require.NotNil(t, resp)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.False(t, deleteCalled, "foreign reservation replacement must not be contacted")
	metadata, err := storage.Storage.LoadMetadata(t.Context(), backendSID)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00foreign", metadata[session.MetadataKeyIdentityBinding])
	require.Equal(t, "replacement", metadata["version"])
	metadata, err = storage.Storage.LoadMetadata(t.Context(), clientSID)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00replacement", metadata[session.MetadataKeyIdentityBinding])
}

func TestBackendRecoveryAliasDoesNotDeleteParentOnFailedUpsert(t *testing.T) {
	t.Parallel()

	const clusterIP = "http://cluster-ip:8080"
	clientSID := uuid.NewString()
	replacement := session.NewProxySession(clientSID)
	replacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00replacement")
	storage := &replacingStorage{
		Storage:          session.NewLocalStorage(),
		replacement:      replacement,
		replaceAfterLoad: true,
	}
	store := session.NewManagerWithStorage(time.Hour, func(id string) session.Session {
		return session.NewProxySession(id)
	}, storage)
	t.Cleanup(func() { require.NoError(t, store.Stop()) })
	original := session.NewProxySession(clientSID)
	original.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	original.SetMetadata(sessionMetadataInitBody, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`)
	require.NoError(t, store.AddSession(original))
	var deleteCalled bool
	r := newRecovery(clusterIP, store, func(req *http.Request) (*http.Response, error) {
		deleteCalled = deleteCalled || req.Method == http.MethodDelete
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       http.NoBody,
			Header:     http.Header{"Mcp-Session-Id": []string{clientSID}},
		}, nil
	})
	req := httptest.NewRequest(http.MethodPost, clusterIP+"/mcp", strings.NewReader(`{"method":"tools/list"}`))
	req.Header.Set("Mcp-Session-Id", clientSID)

	resp, err := r.reinitializeAndReplay(req, []byte(`{"method":"tools/list"}`))

	require.NoError(t, err)
	require.NotNil(t, resp)
	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.False(t, deleteCalled)
	metadata, err := storage.Storage.LoadMetadata(t.Context(), clientSID)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00replacement", metadata[session.MetadataKeyIdentityBinding])
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
	store := newRecoveryStore(t, sess)

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
	store := newRecoveryStore(t, sess)

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
	store := newRecoveryStore(t, sess)

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
