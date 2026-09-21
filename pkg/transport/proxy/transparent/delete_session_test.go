// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package transparent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/transport/session"
)

type replacingStorage struct {
	session.Storage
	replacement           session.Session
	replaceAfterMetadata  bool
	replaceAfterLoad      bool
	unconditionalDelete   bool
	conditionalDeleteErr  error
	replaceBeforeDeleteID string
	deleteReplacement     session.Session
}

func (s *replacingStorage) Load(ctx context.Context, id string) (session.Session, error) {
	stored, err := s.Storage.Load(ctx, id)
	if err != nil || !s.replaceAfterLoad {
		return stored, err
	}
	s.replaceAfterLoad = false
	if err := s.Store(ctx, s.replacement); err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *replacingStorage) LoadIfOwner(
	ctx context.Context, id, expectedOwner string,
) (session.Session, error) {
	stored, err := s.Storage.LoadIfOwner(ctx, id, expectedOwner)
	if err != nil || !s.replaceAfterLoad {
		return stored, err
	}
	s.replaceAfterLoad = false
	if err := s.Store(ctx, s.replacement); err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *replacingStorage) LoadMetadata(ctx context.Context, id string) (map[string]string, error) {
	metadata, err := s.Storage.LoadMetadata(ctx, id)
	if err != nil || !s.replaceAfterMetadata {
		return metadata, err
	}
	s.replaceAfterMetadata = false
	if err := s.Store(ctx, s.replacement); err != nil {
		return nil, err
	}
	return metadata, nil
}

func (s *replacingStorage) DeleteIfOwner(ctx context.Context, id, expectedOwner string) (bool, error) {
	if s.conditionalDeleteErr != nil {
		return false, s.conditionalDeleteErr
	}
	if id == s.replaceBeforeDeleteID {
		s.replaceBeforeDeleteID = ""
		if err := s.Store(ctx, s.deleteReplacement); err != nil {
			return false, err
		}
	}
	return s.Storage.DeleteIfOwner(ctx, id, expectedOwner)
}

func (s *replacingStorage) Delete(ctx context.Context, id string) error {
	s.unconditionalDelete = true
	return s.Storage.Delete(ctx, id)
}

func TestDeleteRoutesValidatedSnapshotAndRejectsRacedCleanup(t *testing.T) {
	t.Parallel()

	id := "cccccccc-0006-0006-0006-000000000006"
	var replacementCalls int
	replacementTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		replacementCalls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(replacementTarget.Close)
	replacement := session.NewProxySession(id)
	replacement.SetMetadata(session.MetadataKeyIdentityBinding, "issuer\x00replacement")
	replacement.SetMetadata(sessionMetadataBackendURL, replacementTarget.URL)
	replacement.SetMetadata(sessionMetadataBackendSID, "replacement-backend-sid")
	replacement.SetMetadata("version", "replacement")
	storage := &replacingStorage{
		Storage:          session.NewLocalStorage(),
		replacement:      replacement,
		replaceAfterLoad: true,
	}
	p := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http",
		nil, nil, "", false, nil, WithSessionStorage(storage),
	)
	t.Cleanup(func() { require.NoError(t, p.sessionManager.Stop()) })
	originalTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, id, r.Header.Get("Mcp-Session-Id"))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(originalTarget.Close)
	original := session.NewProxySession(id)
	original.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	original.SetMetadata(sessionMetadataBackendURL, originalTarget.URL)
	require.NoError(t, p.sessionManager.AddSession(original))
	targetURL, err := url.Parse(originalTarget.URL)
	require.NoError(t, err)
	proxy := createBasicProxy(p, targetURL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, originalTarget.URL, nil)
	req.Header.Set("Mcp-Session-Id", id)

	proxy.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.False(t, storage.replaceAfterLoad)
	require.False(t, storage.unconditionalDelete)
	require.Zero(t, replacementCalls)
	metadata, err := storage.Storage.LoadMetadata(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, "issuer\x00replacement", metadata[session.MetadataKeyIdentityBinding])
	require.Equal(t, "replacement", metadata["version"])
}

func TestDeleteStorageFailureReplacesUpstreamSuccess(t *testing.T) {
	t.Parallel()

	id := "cccccccc-0007-0007-0007-000000000007"
	storage := &replacingStorage{
		Storage:              session.NewLocalStorage(),
		conditionalDeleteErr: errors.New("storage unavailable"),
	}
	p := NewTransparentProxyWithOptions(
		"127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http",
		nil, nil, "", false, nil, WithSessionStorage(storage),
	)
	t.Cleanup(func() { require.NoError(t, p.sessionManager.Stop()) })
	seeded := session.NewProxySession(id)
	seeded.SetMetadata(session.MetadataKeyIdentityBinding, sessionbinding.UnauthenticatedSentinel)
	require.NoError(t, p.sessionManager.AddSession(seeded))
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	targetURL, err := url.Parse(target.URL)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, target.URL, nil)
	req.Header.Set("Mcp-Session-Id", id)

	createBasicProxy(p, targetURL).ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestDeleteSessionCleanup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		seedSession      bool   // whether to pre-populate a session in the manager
		sessionID        string // the session ID to seed and/or reference (must be a valid UUID)
		deleteHeader     string // value of Mcp-Session-Id header on the DELETE request ("" = omit header)
		deleteStatusCode int    // status code the upstream returns for the DELETE
		expectSession    bool   // whether the session should exist after the DELETE
	}{
		{
			name:             "DELETE with 200 removes session",
			seedSession:      true,
			sessionID:        "cccccccc-0001-0001-0001-000000000001",
			deleteHeader:     "cccccccc-0001-0001-0001-000000000001",
			deleteStatusCode: http.StatusOK,
			expectSession:    false,
		},
		{
			name:             "DELETE with 404 removes session",
			seedSession:      true,
			sessionID:        "cccccccc-0002-0002-0002-000000000002",
			deleteHeader:     "cccccccc-0002-0002-0002-000000000002",
			deleteStatusCode: http.StatusNotFound,
			expectSession:    false,
		},
		{
			name:             "DELETE with 500 does not remove session",
			seedSession:      true,
			sessionID:        "cccccccc-0003-0003-0003-000000000003",
			deleteHeader:     "cccccccc-0003-0003-0003-000000000003",
			deleteStatusCode: http.StatusInternalServerError,
			expectSession:    true,
		},
		{
			name:             "DELETE without Mcp-Session-Id header does nothing",
			seedSession:      true,
			sessionID:        "cccccccc-0004-0004-0004-000000000004",
			deleteHeader:     "",
			deleteStatusCode: http.StatusOK,
			expectSession:    true,
		},
		{
			name:             "DELETE for non-existent session does not error",
			seedSession:      false,
			sessionID:        "cccccccc-0005-0005-0005-000000000005",
			deleteHeader:     "cccccccc-0005-0005-0005-000000000005",
			deleteStatusCode: http.StatusOK,
			expectSession:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := NewTransparentProxy("127.0.0.1", 0, "", nil, nil, nil, false, false, "streamable-http", nil, nil, "", false)

			// Seed the session directly in the manager if needed.
			if tt.seedSession {
				seeded := session.NewProxySession(tt.sessionID)
				seeded.SetMetadata(session.MetadataKeyIdentityBinding, "unauthenticated") // Auth-disabled fixture.
				require.NoError(t, p.sessionManager.AddSession(seeded))
			}

			// Create a target server that returns the desired status code for DELETE.
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.deleteStatusCode)
			}))
			defer target.Close()

			targetURL, _ := url.Parse(target.URL)
			proxy := createBasicProxy(p, targetURL)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, target.URL, nil)
			if tt.deleteHeader != "" {
				req.Header.Set("Mcp-Session-Id", tt.deleteHeader)
			}
			proxy.ServeHTTP(rec, req)

			_, ok := p.sessionManager.Get(tt.sessionID)
			assert.Equal(t, tt.expectSession, ok,
				"session existence mismatch: want exists=%v, got exists=%v", tt.expectSession, ok)
		})
	}
}
