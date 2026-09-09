// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/httperr"
	skillsmocks "github.com/stacklok/toolhive/pkg/skills/mocks"
)

// TestRequireLocalKeySigning pins who may name a private key on the server's
// filesystem. A key-bearing push is a request for THIS process to read a key
// and sign with it, so the decision rests on the listener and the kernel's
// view of the peer — never on anything the request can set.
func TestRequireLocalKeySigning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		key        string
		local      localTransport
		remoteAddr string
		wantAllow  bool
	}{
		{
			name:       "no key is not a key-signing request",
			remoteAddr: "203.0.113.7:44321",
			wantAllow:  true,
		},
		{
			// An IPC peer has no host:port to inspect, so the listener is
			// the only thing that can vouch for it.
			name:       "an IPC listener vouches for its peer",
			key:        "/home/dev/cosign.key",
			local:      true,
			remoteAddr: "@",
			wantAllow:  true,
		},
		{
			name:       "a loopback TCP peer is on this machine",
			key:        "/home/dev/cosign.key",
			remoteAddr: "127.0.0.1:53124",
			wantAllow:  true,
		},
		{
			name:       "so is an IPv6 loopback peer",
			key:        "/home/dev/cosign.key",
			remoteAddr: "[::1]:53124",
			wantAllow:  true,
		},
		{
			// The exposure this guard exists for: a non-loopback bind gets
			// no Origin allowlist and assigns a synthetic local identity, so
			// this caller is unauthenticated.
			name:       "a remote peer may not name a key",
			key:        "/home/dev/cosign.key",
			remoteAddr: "203.0.113.7:44321",
			wantAllow:  false,
		},
		{
			// Fail closed: a peer this function cannot judge is not local
			// unless the listener says so.
			name:      "an unjudgeable peer on a TCP listener is refused",
			key:       "/home/dev/cosign.key",
			wantAllow: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/push", nil)
			req.RemoteAddr = tc.remoteAddr

			err := requireLocalKeySigning(req, tc.local, tc.key)
			if tc.wantAllow {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, http.StatusForbidden, httperr.Code(err))
			assert.Contains(t, err.Error(), "identity_token",
				"the refusal must name the credential that does work remotely")
		})
	}
}

// TestRequireLocalKeySigning_HeadersCannotForgeLocality guards the obvious
// bypass. RemoteAddr is the kernel's view of the peer; the headers a proxy
// or a caller can set must not stand in for it.
func TestRequireLocalKeySigning_HeadersCannotForgeLocality(t *testing.T) {
	t.Parallel()

	for _, header := range []string{"X-Forwarded-For", "X-Real-IP", "Forwarded"} {
		t.Run(header, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/push", nil)
			req.RemoteAddr = "203.0.113.7:44321"
			req.Header.Set(header, "127.0.0.1")

			err := requireLocalKeySigning(req, false, "/home/dev/cosign.key")
			require.Error(t, err, "%s must not make a remote caller local", header)
			assert.Equal(t, http.StatusForbidden, httperr.Code(err))
		})
	}
}

// TestSkillsRouter_RemoteKeyPushRejectedBeforeDispatch runs the guard through
// the real router. The mock has no expectations, so reaching the service at
// all fails the test: the refusal has to land before anything opens the key.
func TestSkillsRouter_RemoteKeyPushRejectedBeforeDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		opts       []RouterOption
		remoteAddr string
		body       string
		wantStatus int
	}{
		{
			name:       "remote caller naming a key",
			remoteAddr: "203.0.113.7:44321",
			body:       `{"reference":"ghcr.io/test/skill:v1","key":"/home/dev/cosign.key"}`,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "IPC listener permits the same request",
			opts:       []RouterOption{WithLocalTransport(true)},
			remoteAddr: "@",
			body:       `{"reference":"ghcr.io/test/skill:v1","key":"/home/dev/cosign.key"}`,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "loopback caller permits the same request",
			remoteAddr: "127.0.0.1:53124",
			body:       `{"reference":"ghcr.io/test/skill:v1","key":"/home/dev/cosign.key"}`,
			wantStatus: http.StatusNoContent,
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

			req := httptest.NewRequest(http.MethodPost, "/push", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = tc.remoteAddr
			rec := httptest.NewRecorder()
			SkillsRouter(svc, tc.opts...).ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code, "body: %s", rec.Body.String())
		})
	}
}
