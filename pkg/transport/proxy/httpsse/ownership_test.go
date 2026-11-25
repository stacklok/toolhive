// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package httpsse

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

func TestSessionOwnershipHTTP(t *testing.T) {
	t.Parallel()
	authenticate := types.NamedMiddleware{Name: "test-auth", Function: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.Header.Get("Authorization")
			if token == "" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			iss, sub := "issuer", "owner"
			if token == "foreign" {
				sub = "foreign"
			}
			if token == "other-issuer" {
				iss = "other"
			}
			caller := &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": iss, "sub": sub}}, Token: token}
			next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), caller)))
		})
	}}
	proxy := NewHTTPSSEProxy("127.0.0.1", 0, false, nil, []types.NamedMiddleware{authenticate})
	require.NoError(t, proxy.Start(t.Context()))
	t.Cleanup(func() { require.NoError(t, proxy.Stop(context.Background())) })
	base := "http://" + proxy.server.Addr
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/sse", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "owner")
	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, 200, resp.StatusCode)
	scanner := bufio.NewScanner(resp.Body)
	var endpoint string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			endpoint = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			break
		}
	}
	require.NotEmpty(t, endpoint)
	if strings.HasPrefix(endpoint, "/") {
		endpoint = base + endpoint
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"foreign", 404}, {"other-issuer", 404}, {"", 401}, {"owner", 202}, {"refreshed", 202}} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
		require.NoError(t, err)
		req.Header.Set("Authorization", tc.token)
		response, err := client.Do(req)
		require.NoError(t, err)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
		require.Equal(t, tc.status, response.StatusCode, tc.token)
		if tc.status == 202 {
			select {
			case <-proxy.messageCh:
			case <-time.After(time.Second):
				t.Fatal("owner message not forwarded")
			}
		} else {
			require.Empty(t, proxy.messageCh, "rejected request must not reach backend")
		}
		require.Equal(t, 1, proxy.sessionManager.Count(), "rejected requests must not remove owner's live session")
	}
}
