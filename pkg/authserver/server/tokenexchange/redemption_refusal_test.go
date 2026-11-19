// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	refusalIssuer   = "https://issuer.example.com"
	refusalSubject  = "refusal-subject"
	refusalClientID = "refusal-client"
	refusalResource = "https://resource.example.com"
	// unboundRefusalResource is a resource the listed subject is not bound to.
	unboundRefusalResource = "https://unbound.example.com"
)

func refusalClaims() *ValidatedClaims {
	now := time.Now()
	return &ValidatedClaims{
		Issuer:   refusalIssuer,
		Subject:  refusalSubject,
		ClientID: refusalClientID,
		JWTID:    "refusal-jti",
		IssuedAt: now,
		Expiry:   now.Add(5 * time.Minute),
	}
}

func refusalPolicies(t *testing.T) []TrustedIssuer {
	t.Helper()
	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
		IssuerURL:              refusalIssuer,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: refusalSubject, AllowedResources: []string{refusalResource}},
			},
		},
	}})
	require.NoError(t, err)
	return resolved
}

// errKeysDown is a validator failure that happened before the issuer's keys
// were in hand.
var errKeysDown = fmt.Errorf("%w: connection refused", errIssuerKeysUnavailable)

type refusalCase struct {
	name        string
	validatorEr error
	mutate      func(c *ValidatedClaims)
	form        func(f url.Values)
	wantStatus  int // 0 means 400
	wantCode    string
	wantDesc    string
}

func (tc refusalCase) claims() *ValidatedClaims {
	c := refusalClaims()
	if tc.mutate != nil {
		tc.mutate(c)
	}
	return c
}

func (tc refusalCase) requireRefusal(t *testing.T, err error) {
	t.Helper()
	status := tc.wantStatus
	if status == 0 {
		status = http.StatusBadRequest
	}
	requireRefusalStatus(t, err, status, tc.wantCode, tc.wantDesc)
}

func TestIDJAGRedemptionRefusals(t *testing.T) {
	t.Parallel()

	tests := []refusalCase{
		{
			name: "assertion fails validation", validatorEr: errors.New("signature mismatch"),
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: The ID-JAG assertion is invalid or could not be verified.",
		},
		{
			name: "issuer keys unavailable", validatorEr: errKeysDown,
			wantStatus: http.StatusServiceUnavailable, wantCode: "temporarily_unavailable", wantDesc: "issuer_unavailable",
		},
		{
			name: "issuer not enabled", mutate: func(c *ValidatedClaims) { c.Issuer = "https://unlisted.example.com" },
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: The ID-JAG assertion issuer is not enabled for this grant.",
		},
		{
			name: "assertion expired before issuance", mutate: func(c *ValidatedClaims) { c.Expiry = time.Now().Add(-time.Minute) },
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: The JWT bearer assertion has expired.",
		},
		{
			name: "subject not listed", mutate: func(c *ValidatedClaims) { c.Subject = "unlisted-subject" },
			wantCode: "access_denied", wantDesc: "subject_unknown",
		},
		{
			name:     "requested resource outside the subject binding",
			form:     func(f url.Values) { f.Set("resource", unboundRefusalResource) },
			wantCode: "access_denied", wantDesc: "resource_denied",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			validator := &testJWTBearerAssertionValidator{claims: tt.claims(), err: tt.validatorEr}
			handler, err := newIDJAGIssuanceHandler(validator, testTokenEndpoint,
				&recordingAssertionConsumer{},
				&fosite.Config{AccessTokenLifespan: time.Hour, AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy},
				&mockAccessTokenStrategy{}, &mockAccessTokenStorage{}, refusalPolicies(t), nil, nil, nil)
			require.NoError(t, err)
			req := newIDJAGRequest(t, newTestJWKS(t), refusalClientID)
			req.Form.Set("resource", refusalResource)
			req.Client = &fosite.DefaultClient{ID: refusalClientID, Audience: []string{refusalResource, unboundRefusalResource}}
			if tt.form != nil {
				tt.form(req.Form)
			}

			err = handler.HandleTokenEndpointRequest(context.Background(), req)

			tt.requireRefusal(t, err)
		})
	}
}

func TestJWTBearerRedemptionRefusals(t *testing.T) {
	t.Parallel()

	tests := []refusalCase{
		{
			name: "assertion fails validation", validatorEr: errors.New("signature mismatch"),
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: The JWT bearer assertion is invalid or could not be verified.",
		},
		{
			name: "issuer keys unavailable", validatorEr: errKeysDown,
			wantStatus: http.StatusServiceUnavailable, wantCode: "temporarily_unavailable", wantDesc: "issuer_unavailable",
		},
		{
			name: "assertion expired before issuance", mutate: func(c *ValidatedClaims) { c.Expiry = time.Now().Add(-time.Minute) },
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: The JWT bearer assertion has expired.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			validator := &testJWTBearerAssertionValidator{claims: tt.claims(), err: tt.validatorEr}
			handler, err := newJWTBearerIssuanceHandler(validator, testTokenEndpoint,
				testAssertionJWTConsumer{}, &fosite.Config{AccessTokenLifespan: time.Hour},
				&mockAccessTokenStrategy{}, &mockAccessTokenStorage{}, refusalPolicies(t))
			require.NoError(t, err)
			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
			req.Form = map[string][]string{
				"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
				"resource":  {refusalResource},
			}

			err = handler.HandleTokenEndpointRequest(context.Background(), req)

			tt.requireRefusal(t, err)
		})
	}
}

// TestRedemptionRefusal_Grammar pins the error_description grammar clients
// parse: a lowercase reason, optionally followed by ": " and a hint.
func TestRedemptionRefusal_Grammar(t *testing.T) {
	t.Parallel()

	for _, reason := range []string{reasonInvalidAssertion, reasonSubjectUnknown, reasonResourceDenied, reasonIssuerUnavailable} {
		for _, hint := range []string{"", "A hint."} {
			description := redemptionRefusal("access_denied", reason, hint).GetDescription()
			assert.Regexp(t, `^[a-z_]+(: .*)?$`, description)
			assert.Regexp(t, "^"+reason, description)
		}
	}
}

func TestAssertionValidationRefusal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantDebug  string
	}{
		{
			name: "no default audiences", err: errDefaultAudiencesEmpty,
			wantStatus: http.StatusInternalServerError, wantCode: "server_error", wantDebug: errDefaultAudiencesEmpty.Error(),
		},
		{
			name: "request canceled", err: fmt.Errorf("lookup: %w", context.Canceled),
			wantStatus: http.StatusInternalServerError, wantCode: "server_error", wantDebug: context.Canceled.Error(),
		},
		{
			name: "request deadline", err: fmt.Errorf("lookup: %w", context.DeadlineExceeded),
			wantStatus: http.StatusInternalServerError, wantCode: "server_error", wantDebug: context.DeadlineExceeded.Error(),
		},
		{
			name: "ordinary failure", err: errors.New("signature mismatch"),
			wantStatus: http.StatusBadRequest, wantCode: "invalid_grant",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := assertionValidationRefusal(tt.err, "A hint.")

			var rfcErr *fosite.RFC6749Error
			require.True(t, errors.As(err, &rfcErr))
			assert.Equal(t, tt.wantCode, rfcErr.ErrorField)
			assert.Equal(t, tt.wantStatus, rfcErr.CodeField)
			assert.Contains(t, rfcErr.DebugField, tt.wantDebug)
			if tt.wantDebug != "" {
				assert.ErrorIs(t, err, tt.err)
				assert.NotContains(t, rfcErr.GetDescription(), tt.wantDebug)
			}
		})
	}
}

func TestIssuerUnavailable_Refusal(t *testing.T) {
	t.Parallel()

	cause := fmt.Errorf("%w: failed to fetch JWKS for issuer %s", errIssuerKeysUnavailable, refusalIssuer)
	err := assertionValidationRefusal(cause, "A hint.")

	var rfcErr *fosite.RFC6749Error
	require.True(t, errors.As(err, &rfcErr))
	assert.Equal(t, "temporarily_unavailable", rfcErr.ErrorField)
	assert.Equal(t, http.StatusServiceUnavailable, rfcErr.CodeField)
	assert.Equal(t, reasonIssuerUnavailable, rfcErr.GetDescription())
	// The cause stays wrapped for errors.Is but is not copied into the debug
	// field, so a refused request does not repeat the issuer's error text.
	assert.ErrorIs(t, err, errIssuerKeysUnavailable)
	assert.Empty(t, rfcErr.DebugField)
	assert.NotContains(t, rfcErr.GetDescription(), refusalIssuer)
}
