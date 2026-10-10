// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpsession "github.com/stacklok/toolhive/pkg/vmcp/session"
	vmcpsessionbinding "github.com/stacklok/toolhive/pkg/vmcp/session/binding"
)

// bindingFakeSession is a MultiSession that stubs only the single metadata
// accessor the binding gate reads. The embedded nil interface satisfies the
// rest and panics if the gate reaches a method it must not.
type bindingFakeSession struct {
	vmcpsession.MultiSession
	binding string
}

func (f *bindingFakeSession) GetMetadataValue(key string) (string, bool) {
	if key == vmcpsession.MetadataKeyIdentityBinding {
		return f.binding, true
	}
	return "", false
}

// bindingFakeSessionManager returns a fixed session (or not-found) for any ID.
type bindingFakeSessionManager struct {
	stubSessionManager
	sess vmcpsession.MultiSession
	ok   bool
}

func (m *bindingFakeSessionManager) GetMultiSession(context.Context, string) (vmcpsession.MultiSession, bool) {
	return m.sess, m.ok
}

// TestSessionBindingCallGate verifies the pre-dispatch gate rejects a
// session-scoped LIST request from a caller who is not the session's bound
// owner, while admitting every request that exposes no other session's
// capabilities. Without this gate a foreign caller holding a valid token plus
// the victim's session ID receives the owner's advertised tools/resources/
// prompts (#6779).
func TestSessionBindingCallGate(t *testing.T) {
	t.Parallel()

	const (
		issuer = "https://issuer.example"
		owner  = "user-a"
		other  = "user-b"
	)

	ownerBinding, err := vmcpsessionbinding.Format(issuer, owner)
	require.NoError(t, err)

	// identityFor builds a caller presenting a token whose (iss, sub) claims
	// produce the given session binding.
	identityFor := func(iss, sub string) *auth.Identity {
		return &auth.Identity{Token: "secret", Claims: map[string]any{"iss": iss, "sub": sub}}
	}

	tests := []struct {
		name         string
		parsed       *mcpparser.ParsedMCPRequest
		sessionID    string
		session      vmcpsession.MultiSession
		sessionFound bool
		caller       *auth.Identity
		wantDenial   bool
	}{
		{
			name:   "no parsed request admits",
			caller: identityFor(issuer, other),
		},
		{
			name:   "non-list method admits (gate ignores call path)",
			parsed: &mcpparser.ParsedMCPRequest{Method: "tools/call", ResourceID: "t"},
			caller: identityFor(issuer, other),
		},
		{
			name:         "list method without session header admits",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			sessionFound: true,
			session:      &bindingFakeSession{binding: ownerBinding},
			caller:       identityFor(issuer, other),
		},
		{
			name:         "list method for unknown session admits",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			sessionID:    "s-unknown",
			sessionFound: false,
			caller:       identityFor(issuer, other),
		},
		{
			name:         "list method by owner admits",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			sessionID:    "s-owner",
			sessionFound: true,
			session:      &bindingFakeSession{binding: ownerBinding},
			caller:       identityFor(issuer, owner),
		},
		{
			name:         "list method by foreign caller is denied",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			sessionID:    "s-owner",
			sessionFound: true,
			session:      &bindingFakeSession{binding: ownerBinding},
			caller:       identityFor(issuer, other),
			wantDenial:   true,
		},
		{
			name:         "resources/list by foreign caller is denied",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "resources/list"},
			sessionID:    "s-owner",
			sessionFound: true,
			session:      &bindingFakeSession{binding: ownerBinding},
			caller:       identityFor(issuer, other),
			wantDenial:   true,
		},
		{
			name:         "resources/templates/list by foreign caller is denied",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "resources/templates/list"},
			sessionID:    "s-owner",
			sessionFound: true,
			session:      &bindingFakeSession{binding: ownerBinding},
			caller:       identityFor(issuer, other),
			wantDenial:   true,
		},
		{
			name:         "prompts/list by foreign caller is denied",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "prompts/list"},
			sessionID:    "s-owner",
			sessionFound: true,
			session:      &bindingFakeSession{binding: ownerBinding},
			caller:       identityFor(issuer, other),
			wantDenial:   true,
		},
		{
			name:         "anonymous session admits anonymous caller",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			sessionID:    "s-anon",
			sessionFound: true,
			session:      &bindingFakeSession{binding: vmcpsessionbinding.UnauthenticatedSentinel},
			caller:       nil,
		},
		{
			name:         "anonymous session rejects token-bearing caller (upgrade attack)",
			parsed:       &mcpparser.ParsedMCPRequest{Method: "tools/list"},
			sessionID:    "s-anon",
			sessionFound: true,
			session:      &bindingFakeSession{binding: vmcpsessionbinding.UnauthenticatedSentinel},
			caller:       &auth.Identity{Token: "secret"},
			wantDenial:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			if tt.parsed != nil {
				ctx = context.WithValue(ctx, mcpparser.MCPRequestContextKey, tt.parsed)
			}
			if tt.caller != nil {
				ctx = auth.WithIdentity(ctx, tt.caller)
			}

			s := &Server{
				vmcpSessionMgr: &bindingFakeSessionManager{
					stubSessionManager: stubSessionManager{alive: tt.sessionFound},
					sess:               tt.session,
					ok:                 tt.sessionFound,
				},
			}
			gate := s.sessionBindingCallGate()

			req, reqErr := http.NewRequest(http.MethodPost, "/mcp", nil)
			require.NoError(t, reqErr)
			if tt.sessionID != "" {
				req.Header.Set(mcpSessionIDHeader, tt.sessionID)
			}

			denial := gate(ctx, req)

			if tt.wantDenial {
				require.NotNil(t, denial, "expected a denial")
				assert.Equal(t, mcpparser.JSONRPCCodeDenied, denial.Code, "denial code must be 403")
				assert.Equal(t, 0, denial.HTTPStatus, "HTTPStatus must be zero so the shim writes 403")
				assert.Equal(t, vmcp.DenyMessageSessionBinding, denial.Message)
			} else {
				assert.Nil(t, denial, "expected the request to be admitted")
			}
		})
	}

	// A nil *http.Request (unit-call shape) must be admitted, not panic.
	t.Run("nil request admits", func(t *testing.T) {
		t.Parallel()

		ctx := context.WithValue(t.Context(), mcpparser.MCPRequestContextKey,
			&mcpparser.ParsedMCPRequest{Method: "tools/list"})
		s := &Server{vmcpSessionMgr: &bindingFakeSessionManager{}}
		assert.Nil(t, s.sessionBindingCallGate()(ctx, nil))
	})
}
