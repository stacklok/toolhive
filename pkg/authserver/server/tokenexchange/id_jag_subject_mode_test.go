// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
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
	return deps
}

func TestIDJAGHandler_SubjectModeGate(t *testing.T) {
	t.Parallel()

	bound := func(subject string) []JWTBearerSubjectBinding {
		return []JWTBearerSubjectBinding{{Subject: subject, AllowedResources: []string{idJAGTestResource}}}
	}
	tests := []struct {
		name           string
		policy         *JWTBearerGrantPolicy
		clientAudience []string // overrides the client's audiences when set
		wantReason     string   // the access_denied reason; empty means no refusal
		wantConsumed   bool
	}{
		{
			name:         "namespaced issuer with a matching binding redeems",
			policy:       subjectModePolicy(SubjectModeNamespaced, bound(idJAGTestSubject)),
			wantConsumed: true,
		},
		{
			name:       "namespaced issuer still checks the binding",
			policy:     subjectModePolicy("", bound("someone-else")),
			wantReason: "subject_unknown",
		},
		{
			name:       "resolve issuer is refused at the gate",
			policy:     subjectModePolicy(SubjectModeResolve, nil),
			wantReason: "subject_unknown",
		},
		{
			// The gate runs after the client-audience check, so an earlier
			// refusal keeps its own reason.
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
			h := newQuietIDJAGHandler(t, subjectModeDeps(t, tt.policy, consumer))
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			if tt.clientAudience != nil {
				req.Client = &fosite.DefaultClient{ID: idJAGTestClientID, Audience: tt.clientAudience}
			}
			original := req.GetSession()

			err := h.HandleTokenEndpointRequest(context.Background(), req)

			if tt.wantReason != "" {
				requireRefusal(t, err, "access_denied", tt.wantReason)
			} else {
				require.NoError(t, err)
			}
			if tt.wantConsumed {
				assert.Equal(t, "id-jag-jti", consumer.key)
				issued, ok := req.GetSession().(*session.Session)
				require.True(t, ok)
				assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, issued.GetSubject())
				return
			}
			assert.Empty(t, consumer.key, "the ID-JAG must stay unused")
			assert.Same(t, original, req.GetSession(), "no session may be installed")
		})
	}
}

// TestIDJAGHandler_ResolveIssuerReachesGate shows the gate is the only thing
// that refuses a valid resolve redemption: the policy checks pass.
func TestIDJAGHandler_ResolveIssuerReachesGate(t *testing.T) {
	t.Parallel()

	deps := subjectModeDeps(t, subjectModePolicy(SubjectModeResolve, nil), &recordingAssertionConsumer{})
	h := newQuietIDJAGHandler(t, deps)
	req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	// The resolved policy carries maxAssertionAge; the raw one does not.
	require.Len(t, deps.trustedIssuers, 1)
	policy := deps.trustedIssuers[0].JWTBearerGrant

	resource, err := h.checkIDJAGPolicy(req, validIDJAGClaims(), policy)

	require.NoError(t, err)
	assert.Equal(t, idJAGTestResource, resource)
}

func TestCheckSubjectModeGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        SubjectMode
		wantRefusal bool
	}{
		{"empty mode passes", "", false},
		{"namespaced passes", SubjectModeNamespaced, false},
		{"resolve is refused", SubjectModeResolve, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkSubjectModeGate(&JWTBearerGrantPolicy{SubjectMode: tt.mode})
			if tt.wantRefusal {
				requireRefusal(t, err, "access_denied", "subject_unknown")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIDJAGConstructor_SubjectResolutionDisabledWarn(t *testing.T) {
	t.Parallel()

	const (
		otherResolveIssuer = "https://a-idp.example.com"
		namespacedIssuer   = "https://z-idp.example.com"
		wantMsg            = `level=WARN msg="Resolution of the subject at ID-JAG redemption is not enabled in this build; ` +
			`redemptions from resolve issuers are refused" `
	)
	bindings := []JWTBearerSubjectBinding{{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}}}
	issuer := func(url string, mode SubjectMode) TrustedIssuer {
		policy := subjectModePolicy(mode, nil)
		if mode != SubjectModeResolve {
			policy.SubjectBindings = bindings
		}
		return TrustedIssuer{IssuerURL: url, AllowedDelegateClients: []string{anyDelegateClient}, JWTBearerGrant: policy}
	}
	tests := []struct {
		name    string
		issuers []TrustedIssuer
		want    []string
	}{
		{
			name:    "resolve issuer is named in one warning",
			issuers: []TrustedIssuer{issuer(idJAGTestIssuer, SubjectModeResolve)},
			want:    []string{wantMsg + "issuers=[" + idJAGTestIssuer + "]"},
		},
		{
			name: "resolve issuers are sorted and namespaced ones left out",
			issuers: []TrustedIssuer{
				issuer(namespacedIssuer, SubjectModeNamespaced),
				issuer(idJAGTestIssuer, SubjectModeResolve),
				issuer(otherResolveIssuer, SubjectModeResolve),
			},
			want: []string{wantMsg + `issuers="[` + otherResolveIssuer + " " + idJAGTestIssuer + `]"`},
		},
		{
			name:    "namespaced issuers log nothing",
			issuers: []TrustedIssuer{issuer(idJAGTestIssuer, SubjectModeNamespaced), issuer(namespacedIssuer, "")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolved, err := ResolveJWTBearerGrantPolicies(tt.issuers)
			require.NoError(t, err)
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: validIDJAGClaims()}, resolved)
			var buf strings.Builder
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
				ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
					if len(groups) == 0 && a.Key == slog.TimeKey {
						return slog.Attr{}
					}
					return a
				},
			}))

			_, err = newIDJAGIssuanceHandler(deps, nil, logger)
			require.NoError(t, err)

			warns := logLinesAt(&buf, "WARN")
			if tt.want == nil {
				assert.Empty(t, warns)
				return
			}
			assert.Equal(t, tt.want, warns)
		})
	}
}

// newQuietIDJAGHandler builds the handler with a discarding logger, so the
// startup WARN for a resolve issuer does not reach the test output.
func newQuietIDJAGHandler(t *testing.T, deps issuanceDeps) *IDJAGHandler {
	t.Helper()
	handler, err := newIDJAGIssuanceHandler(deps, nil, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return handler
}
