// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/server/discovery"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
)

func TestRequireKeySigningCapability(t *testing.T) {
	t.Parallel()

	const capability = "protected-discovery-capability"
	tests := []struct {
		name               string
		key                string
		expectedCapability string
		suppliedCapability string
		remoteAddr         string
		wantAllow          bool
	}{
		{
			name:       "no key needs no capability",
			remoteAddr: "203.0.113.7:44321",
			wantAllow:  true,
		},
		{
			name:               "matching capability authorizes key signing",
			key:                "/home/dev/cosign.key",
			expectedCapability: capability,
			suppliedCapability: capability,
			remoteAddr:         "203.0.113.7:44321",
			wantAllow:          true,
		},
		{
			name:               "missing capability is refused",
			key:                "/home/dev/cosign.key",
			expectedCapability: capability,
			remoteAddr:         "203.0.113.7:44321",
		},
		{
			name:               "wrong capability is refused",
			key:                "/home/dev/cosign.key",
			expectedCapability: capability,
			suppliedCapability: "wrong-capability",
			remoteAddr:         "203.0.113.7:44321",
		},
		{
			name:               "empty configured capability fails closed",
			key:                "/home/dev/cosign.key",
			suppliedCapability: capability,
			remoteAddr:         "203.0.113.7:44321",
		},
		{
			name:               "loopback alone is not authorization",
			key:                "/home/dev/cosign.key",
			expectedCapability: capability,
			remoteAddr:         "127.0.0.1:53124",
		},
		{
			name:               "IPC-shaped peer alone is not authorization",
			key:                "/home/dev/cosign.key",
			expectedCapability: capability,
			remoteAddr:         "@",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/push", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.suppliedCapability != "" {
				req.Header.Set(discovery.KeySigningCapabilityHeader, tc.suppliedCapability)
			}

			err := requireKeySigningCapability(req, tc.expectedCapability, tc.key)
			if tc.wantAllow {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, http.StatusForbidden, httperr.Code(err))
			// The CLI relays this body verbatim, so the remediation has to be
			// runnable from a terminal, not only from a JSON request.
			assert.Contains(t, err.Error(), "--identity-token",
				"the refusal must name the CLI flag that does work remotely")
			assert.Contains(t, err.Error(), "drop --key",
				"omitting --key is the zero-config keyless path and must be offered")
			assert.Contains(t, err.Error(), "identity_token",
				"direct API callers need the request field named too")
			assert.NotContains(t, err.Error(), capability)
		})
	}
}

func TestSkillsRouter_KeySigningCapabilityCheckedBeforeDispatch(t *testing.T) {
	t.Parallel()

	const capability = "protected-discovery-capability"
	tests := []struct {
		name               string
		suppliedCapability string
		remoteAddr         string
		wantStatus         int
	}{
		{
			name:       "loopback caller without capability",
			remoteAddr: "127.0.0.1:53124",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "IPC-shaped caller without capability",
			remoteAddr: "@",
			wantStatus: http.StatusForbidden,
		},
		{
			name:               "caller with matching capability",
			suppliedCapability: capability,
			remoteAddr:         "203.0.113.7:44321",
			wantStatus:         http.StatusNoContent,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			svc := skillsmocks.NewMockSkillService(ctrl)
			if tc.wantStatus == http.StatusNoContent {
				svc.EXPECT().Push(gomock.Any(), gomock.Any()).Return(nil)
			}

			body := `{"reference":"ghcr.io/test/skill:v1","key":"/home/dev/cosign.key"}`
			req := httptest.NewRequest(http.MethodPost, "/push", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if tc.suppliedCapability != "" {
				req.Header.Set(discovery.KeySigningCapabilityHeader, tc.suppliedCapability)
			}
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()
			SkillsRouter(svc, WithKeySigningCapability(capability)).ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code, "body: %s", rec.Body.String())
		})
	}
}

// TestSkillsRouter_ReverseProxyCannotForgeKeySigningAuthorization covers the
// deployment that defeats peer-address checks: an internet-facing reverse
// proxy reaches the API over loopback, so the backend sees a local TCP peer.
// The request must still be refused before the service can open the key.
func TestSkillsRouter_ReverseProxyCannotForgeKeySigningAuthorization(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	svc := skillsmocks.NewMockSkillService(ctrl)
	router := SkillsRouter(svc, WithKeySigningCapability("protected-discovery-capability"))

	var backendRemoteAddr string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendRemoteAddr = r.RemoteAddr
		router.ServeHTTP(w, r)
	}))
	t.Cleanup(backend.Close)

	backendURL, err := url.Parse(backend.URL)
	require.NoError(t, err)
	proxy := httptest.NewServer(httputil.NewSingleHostReverseProxy(backendURL))
	t.Cleanup(proxy.Close)

	body := `{"reference":"ghcr.io/test/skill:v1","key":"/home/dev/cosign.key"}`
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, proxy.URL+"/push", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)

	host, _, err := net.SplitHostPort(backendRemoteAddr)
	require.NoError(t, err)
	ip := net.ParseIP(host)
	require.NotNil(t, ip)
	assert.True(t, ip.IsLoopback(), "backend peer %q should demonstrate the proxy appears local", backendRemoteAddr)
}
