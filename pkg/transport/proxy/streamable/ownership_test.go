// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package streamable

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

type replaceAfterMetadataStorage struct {
	session.Storage
	replacement         session.Session
	replaced            bool
	unconditionalDelete bool
}

func (s *replaceAfterMetadataStorage) LoadMetadata(ctx context.Context, id string) (map[string]string, error) {
	metadata, err := s.Storage.LoadMetadata(ctx, id)
	if err != nil || s.replaced {
		return metadata, err
	}
	s.replaced = true
	if err := s.Store(ctx, s.replacement); err != nil {
		return nil, err
	}
	return metadata, nil
}

func (s *replaceAfterMetadataStorage) Delete(ctx context.Context, id string) error {
	s.unconditionalDelete = true
	return s.Storage.Delete(ctx, id)
}

func TestHandleDeletePreservesReplacementAfterOwnershipCheck(t *testing.T) {
	t.Parallel()

	id := uuid.NewString()
	replacement := session.NewStreamableSession(id)
	replacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00replacement")
	replacement.SetMetadata("version", "replacement")
	storage := &replaceAfterMetadataStorage{
		Storage:     session.NewLocalStorage(),
		replacement: replacement,
	}
	proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil, WithSessionStorage(storage))
	t.Cleanup(func() { require.NoError(t, proxy.Stop(context.Background())) })
	original := session.NewStreamableSession(id)
	original.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	require.NoError(t, proxy.sessionManager.AddSession(original))
	req := httptest.NewRequest(http.MethodDelete, StreamableHTTPEndpoint, nil)
	req.Header.Set("Mcp-Session-Id", id)
	rec := httptest.NewRecorder()

	proxy.handleDelete(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.True(t, storage.replaced)
	require.False(t, storage.unconditionalDelete)
	metadata, err := storage.Storage.LoadMetadata(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00replacement", metadata[session.MetadataKeyIdentityBinding])
	require.Equal(t, "replacement", metadata["version"])
}

func TestSessionlessLegacyMessagesAreRejectedBeforeForwarding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "initialized notification", body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`},
		{name: "other notification", body: `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`},
		{name: "client response", body: `{"jsonrpc":"2.0","id":1,"result":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil)
			t.Cleanup(func() { require.NoError(t, proxy.Stop(context.Background())) })
			req := httptest.NewRequest(http.MethodPost, StreamableHTTPEndpoint, strings.NewReader(tc.body))
			rec := httptest.NewRecorder()

			proxy.handlePost(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Empty(t, proxy.messageCh)
			require.Equal(t, 0, proxy.sessionManager.Count())
		})
	}
}

func TestSessionOwnershipHTTP(t *testing.T) {
	t.Parallel()
	proxy := NewHTTPProxy("127.0.0.1", 0, nil, nil)
	t.Cleanup(func() { require.NoError(t, proxy.Stop(context.Background())) })
	caller := func(token string) *auth.Identity {
		iss, sub := "issuer", "owner"
		if token == "foreign" {
			sub = "foreign"
		}
		if token == "other-issuer" {
			iss = "other"
		}
		return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": iss, "sub": sub}}, Token: token}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		proxy.handleStreamableRequest(w, r.WithContext(auth.WithIdentity(r.Context(), caller(token))))
	}))
	t.Cleanup(server.Close)
	sid := uuid.NewString()
	initReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	initReq = initReq.WithContext(auth.WithIdentity(initReq.Context(), caller("owner")))
	require.NoError(t, proxy.ensureSession(initReq, sid))
	owner, err := proxy.sessionManager.LookupOwner(t.Context(), sid)
	require.NoError(t, err)
	require.NoError(t, sessionbinding.Validate(owner, caller("refresh")))
	client := &http.Client{Timeout: 5 * time.Second}
	send := func(method, token, body string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/mcp", strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Mcp-Session-Id", sid)
		req.Header.Set("Authorization", token)
		resp, err := client.Do(req)
		require.NoError(t, err)
		return resp
	}
	stream := send(http.MethodGet, "owner", "")
	require.Equal(t, 200, stream.StatusCode)
	t.Cleanup(func() { _ = stream.Body.Close() })
	bodies := []string{`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{"jsonrpc":"2.0","id":1,"result":{}}`, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`}
	for _, token := range []string{"foreign", "other-issuer", ""} {
		for _, method := range []string{http.MethodGet, http.MethodDelete, http.MethodPost} {
			for _, body := range bodies {
				resp := send(method, token, body)
				_, err := io.Copy(io.Discard, resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				want := 404
				if token == "" {
					want = 401
				}
				require.Equal(t, want, resp.StatusCode)
				require.Empty(t, proxy.messageCh, "rejected requests must never send upstream")
				require.Equal(t, 1, proxy.serverStreams.streamCount(), "foreign GET/DELETE must not evict owner's stream")
				_, exists := proxy.sessionManager.Get(sid)
				require.True(t, exists)
			}
		}
	}
	// The original owner connection, not a replacement, must still receive data.
	notification, err := jsonrpc2.NewNotification("notifications/tools/list_changed", nil)
	require.NoError(t, err)
	proxy.serverStreams.dispatchTo(sid, notification)
	scanner := bufio.NewScanner(stream.Body)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data:") {
			require.Contains(t, scanner.Text(), "notifications/tools/list_changed")
			break
		}
	}
	require.NoError(t, scanner.Err())
	require.Contains(t, scanner.Text(), "notifications/tools/list_changed")
	// A refreshed owner's notification still forwards after every failed attack.
	resp := send(http.MethodPost, "refreshed", bodies[1])
	require.Equal(t, 202, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	select {
	case <-proxy.messageCh:
	case <-time.After(time.Second):
		t.Fatal("owner notification not sent")
	}
	resp = send(http.MethodDelete, "refreshed", "")
	require.Equal(t, 204, resp.StatusCode)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, 0, proxy.serverStreams.streamCount())
	_, exists := proxy.sessionManager.Get(sid)
	require.False(t, exists)
}
