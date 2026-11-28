// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	subjectmocks "github.com/stacklok/toolhive/pkg/authserver/subject/mocks"
)

func subjectModePolicy(mode SubjectMode, bindings []JWTBearerSubjectBinding) *JWTBearerGrantPolicy {
	return &JWTBearerGrantPolicy{
		MaxAssertionAge:        time.Hour.String(),
		SubjectBindings:        bindings,
		AcceptedAssertionTypes: []JWTBearerAssertionType{JWTBearerAssertionTypeIDJAG},
		SubjectMode:            mode,
	}
}

func subjectModeDeps(
	t *testing.T, policy *JWTBearerGrantPolicy, consumer *recordingAssertionConsumer,
) issuanceDeps {
	t.Helper()
	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
		IssuerURL:              idJAGTestIssuer,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant:         policy,
	}})
	require.NoError(t, err)
	deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, resolved)
	deps.consumer = consumer
	deps.allowedAudiences = []string{idJAGTestResource}
	return deps
}

func TestIDJAGHandler_SubjectMode(t *testing.T) {
	t.Parallel()

	bound := func(sub string) []JWTBearerSubjectBinding {
		return []JWTBearerSubjectBinding{{Subject: sub, AllowedResources: []string{idJAGTestResource}}}
	}
	tests := []struct {
		name           string
		policy         *JWTBearerGrantPolicy
		resolveErr     error    // what the resolver returns for a resolve issuer
		clientAudience []string // overrides the client's audiences when set
		wantSubject    string   // the issued sub; empty means no session
		wantReason     string   // the access_denied reason; empty means no refusal
		wantResolves   int
	}{
		{
			name:        "namespaced issuer with a matching binding redeems",
			policy:      subjectModePolicy(SubjectModeNamespaced, bound(idJAGTestSubject)),
			wantSubject: idJAGTestIssuer + "#" + idJAGTestSubject,
		},
		{
			name:       "namespaced issuer still checks the binding",
			policy:     subjectModePolicy("", bound("someone-else")),
			wantReason: "subject_unknown",
		},
		{
			name:         "resolve issuer issues the resolved user",
			policy:       subjectModePolicy(SubjectModeResolve, nil),
			wantSubject:  resolveTestUser,
			wantResolves: 1,
		},
		{
			name:         "resolve issuer refuses an unknown subject",
			policy:       subjectModePolicy(SubjectModeResolve, nil),
			resolveErr:   subject.ErrUnknown,
			wantReason:   "subject_unknown",
			wantResolves: 1,
		},
		{
			// The client-audience check runs before resolution, so an earlier
			// refusal keeps its own reason and the resolver is not asked.
			name:           "resolve issuer gets the client-audience refusal first",
			policy:         subjectModePolicy(SubjectModeResolve, nil),
			clientAudience: []string{"https://other.example.com"},
			wantReason:     "resource_denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			consumer := &recordingAssertionConsumer{}
			deps := subjectModeDeps(t, tt.policy, consumer)
			claims := resolveClaims(validUpstreamSubject())
			claims.Extra["act"] = map[string]any{"sub": "a"}
			deps.validator = &testJWTBearerAssertionValidator{claims: claims}
			h := newQuietIDJAGHandler(t, deps)
			resolver := subjectmocks.NewMockResolver(gomock.NewController(t))
			user := resolveTestUser
			if tt.resolveErr != nil {
				user = ""
			}
			resolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(user, tt.resolveErr).Times(tt.wantResolves)
			h.resolver = resolver
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			if tt.clientAudience != nil {
				req.Client = &fosite.DefaultClient{ID: idJAGTestClientID, Audience: tt.clientAudience}
			}
			original := req.GetSession()

			err := h.HandleTokenEndpointRequest(context.Background(), req)

			if tt.wantReason != "" {
				requireRefusal(t, err, "access_denied", tt.wantReason)
				assert.Empty(t, consumer.key, "the ID-JAG must stay unused")
				assert.Same(t, original, req.GetSession(), "no session may be installed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "id-jag-jti", consumer.key)
			issued, ok := req.GetSession().(*session.Session)
			require.True(t, ok)
			assert.Equal(t, tt.wantSubject, issued.GetSubject())
		})
	}
}

// newQuietIDJAGHandler builds the handler with a discarding logger.
func newQuietIDJAGHandler(t *testing.T, deps issuanceDeps) *IDJAGHandler {
	t.Helper()
	handler, err := newIDJAGIssuanceHandler(deps, nil, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return handler
}
