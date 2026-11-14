// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testTUIClientID   = "tui-client"
	testOtherClientID = "unlisted-client"
	testAPIAudience   = "https://api.example.com"
)

func requireHint(t *testing.T, err error, want string) {
	t.Helper()
	require.Error(t, err)
	var rfcErr *fosite.RFC6749Error
	require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
	assert.Contains(t, rfcErr.Reason(), want)
}

func sourcePolicyHandler(t *testing.T, tj *testJWKS, policy *ConfiguredClientPolicy) *Handler {
	t.Helper()
	h := newTestHandler(t, tj, 15*time.Minute)
	h.configuredDelegateClients = []string{testAgentClientID}
	h.allowedAudiences = []string{testIssuer, testAPIAudience}
	if policy != nil {
		h.clientPolicies = map[string]ConfiguredClientPolicy{testAgentClientID: *policy}
	}
	return h
}

func TestTokenExchangeHandler_SourcePolicy(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	policy := &ConfiguredClientPolicy{AllowedSourceClients: []string{testTUIClientID}}

	tests := []struct {
		name         string
		policy       *ConfiguredClientPolicy
		tokenClient  string
		mayActSub    string
		wantErr      bool
		hintContains string
	}{
		{name: "token issued to the client itself", policy: policy, tokenClient: testAgentClientID},
		{name: "token issued to a listed source client", policy: policy, tokenClient: testTUIClientID},
		{
			name: "token issued to an unlisted client", policy: policy, tokenClient: testOtherClientID,
			wantErr: true, hintContains: "not issued to this client or to one of its allowed source clients",
		},
		{
			name: "listed source token with a may_act naming another actor", policy: policy,
			tokenClient: testTUIClientID, mayActSub: "someone-else",
			wantErr: true, hintContains: "does not authorize this client",
		},
		{
			name: "listed source token with a matching may_act", policy: policy,
			tokenClient: testTUIClientID, mayActSub: testAgentClientID,
		},
		{
			name: "unlisted client token with a matching may_act", policy: policy,
			tokenClient: testOtherClientID, mayActSub: testAgentClientID,
			wantErr: true, hintContains: "not issued to this client or to one of its allowed source clients",
		},
		{
			name: "empty block allows only the client itself", policy: &ConfiguredClientPolicy{},
			tokenClient: testTUIClientID, wantErr: true, hintContains: "allowed source clients",
		},
		{name: "no policy keeps blanket trust", policy: nil, tokenClient: testOtherClientID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			extra := validExtraClaims()
			extra["client_id"] = tt.tokenClient
			if tt.mayActSub != "" {
				extra["may_act"] = map[string]any{"sub": tt.mayActSub}
			}
			req := newAccessRequest(t, defaultClient(), url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {tj.signToken(t, validClaims(), extra)},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"resource":           {testIssuer},
			})

			err := sourcePolicyHandler(t, tj, tt.policy).HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr {
				requireHint(t, err, tt.hintContains)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestTokenExchangeHandler_SourcePolicyExternalIssuer(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)

	claims := externalClaims()
	claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
	subjectToken := externalJWKS.signToken(t, claims, map[string]any{"azp": "trusted-app"})

	tests := []struct {
		name             string
		allowedDelegates []string
		wantErr          bool
	}{
		{name: "issuer policy names the client", allowedDelegates: []string{testAgentClientID}},
		{name: "issuer policy does not name the client", allowedDelegates: []string{"someone-else"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
				IssuerURL:              testExternalIssuer,
				ExpectedAudience:       testExternalAudience,
				JWKSURL:                jwksServer.URL + "/jwks",
				ActorMatcher:           `claims.azp == "trusted-app"`,
				AllowedDelegateClients: tt.allowedDelegates,
			}})
			h := newTestHandlerWithValidator(validator)
			h.configuredDelegateClients = []string{testAgentClientID}
			// The subject token carries none of these audiences: the check applies
			// to self-issued subject tokens only.
			h.clientPolicies = map[string]ConfiguredClientPolicy{testAgentClientID: {
				SubjectTokenAudiences: []string{"https://not-in-token.example.com"},
			}}

			req := newAccessRequest(t, defaultClient(), url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {subjectToken},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
			})
			err := h.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr {
				requireHint(t, err, "not authorized to exchange subject tokens")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestTokenExchangeHandler_SubjectTokenAudiences(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	const otherAudience = "https://other.example.com"
	listed := []string{testAPIAudience}

	tests := []struct {
		name         string
		policy       *ConfiguredClientPolicy
		subjectAud   []string
		subjectExtra map[string]any
		wantErr      bool
	}{
		{
			name:       "subject token carrying a listed audience accepted",
			policy:     &ConfiguredClientPolicy{SubjectTokenAudiences: listed},
			subjectAud: []string{testIssuer, testAPIAudience},
		},
		{
			name:       "subject token carrying only another audience refused",
			policy:     &ConfiguredClientPolicy{SubjectTokenAudiences: listed},
			subjectAud: []string{testIssuer, otherAudience}, wantErr: true,
		},
		{
			name:       "audience matches exactly, not by path prefix",
			policy:     &ConfiguredClientPolicy{SubjectTokenAudiences: listed},
			subjectAud: []string{testIssuer, testAPIAudience + "/v1"}, wantErr: true,
		},
		{
			name:       "client's own exchanged token without a listed audience refused",
			policy:     &ConfiguredClientPolicy{SubjectTokenAudiences: listed},
			subjectAud: []string{testIssuer},
			subjectExtra: map[string]any{
				"act": map[string]any{"iss": testIssuer, "sub": testAgentClientID},
			},
			wantErr: true,
		},
		{
			name:       "no subject token audiences sets no requirement for any audience",
			policy:     &ConfiguredClientPolicy{AllowedSourceClients: []string{testTUIClientID}},
			subjectAud: []string{testIssuer, otherAudience},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validClaims()
			claims.Audience = jwt.Audience(tt.subjectAud)
			extra := validExtraClaims()
			for k, v := range tt.subjectExtra {
				extra[k] = v
			}
			req := newAccessRequest(t, defaultClient(), url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {tj.signToken(t, claims, extra)},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"resource":           {testIssuer},
			})

			err := sourcePolicyHandler(t, tj, tt.policy).HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr {
				requireHint(t, err, "not issued for one of this client's accepted audiences")
				var rfcErr *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfcErr)
				assert.Equal(t, fosite.ErrInvalidGrant.ErrorField, rfcErr.ErrorField)
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestTokenExchangeHandler_InputAudienceNotObtainable pins the issue case: a
// refresh-token-only client has no output audiences, so a subject token that
// covers its input audience still cannot be exchanged for an access token for
// that audience, however the audience is requested.
func TestTokenExchangeHandler_InputAudienceNotObtainable(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	policy := &ConfiguredClientPolicy{
		AllowedSourceClients:  []string{testTUIClientID},
		SubjectTokenAudiences: []string{testAPIAudience},
	}

	tests := []struct {
		name        string
		form        url.Values
		soleDefault bool
		// requestedAudience stands in for the audience parameter, which fosite
		// copies into the request before the handler runs.
		requestedAudience []string
		// clientAudience is the client's output audiences; the positive rows
		// pin the refusals to it being empty.
		clientAudience []string
		wantCode       string // empty means the exchange succeeds
		wantHint       string
	}{
		{
			name: "audience parameter", requestedAudience: []string{testAPIAudience},
			wantCode: "invalid_target", wantHint: "The client is not permitted to request audience",
		},
		{
			name: "resource parameter", form: url.Values{"resource": {testAPIAudience}},
			wantCode: "invalid_target", wantHint: "The client is not permitted to request resource",
		},
		{
			name: "sole allowed audience as the default", soleDefault: true,
			wantCode: fosite.ErrInvalidRequest.ErrorField,
			wantHint: "The client is not permitted to request a token for the default audience",
		},
		{
			name:              "audience parameter succeeds when the client's audiences include it",
			requestedAudience: []string{testAPIAudience}, clientAudience: []string{testAPIAudience},
		},
		{
			name: "resource parameter succeeds when the client's audiences include it",
			form: url.Values{"resource": {testAPIAudience}}, clientAudience: []string{testAPIAudience},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := sourcePolicyHandler(t, tj, policy)
			if tt.soleDefault {
				h.allowedAudiences = []string{testAPIAudience}
			}
			client := defaultClient()
			client.Audience = tt.clientAudience

			claims := validClaims()
			claims.Audience = jwt.Audience{testIssuer, testAPIAudience}
			extra := validExtraClaims()
			extra["client_id"] = testTUIClientID
			form := url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {tj.signToken(t, claims, extra)},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
			}
			for k, v := range tt.form {
				form[k] = v
			}
			req := newAccessRequest(t, client, form)
			if tt.requestedAudience != nil {
				req.SetRequestedAudience(tt.requestedAudience)
			}

			err := h.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantCode == "" {
				require.NoError(t, err)
				assert.Contains(t, req.GetGrantedAudience(), testAPIAudience)
				return
			}
			var rfcErr *fosite.RFC6749Error
			require.ErrorAs(t, err, &rfcErr)
			assert.Equal(t, tt.wantCode, rfcErr.ErrorField)
			assert.Contains(t, rfcErr.Reason(), tt.wantHint)
			assert.Empty(t, req.GetGrantedAudience())
		})
	}
}
