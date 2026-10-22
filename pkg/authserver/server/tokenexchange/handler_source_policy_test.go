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
			h.clientPolicies = map[string]ConfiguredClientPolicy{testAgentClientID: {}}

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

// TestTokenExchangeHandler_APIAudienceNotForExternalIssuerTokens pins that the
// api_audience exception does not extend to external-issuer subject tokens.
func TestTokenExchangeHandler_APIAudienceNotForExternalIssuerTokens(t *testing.T) {
	t.Parallel()

	selfJWKS := newTestJWKS(t)
	externalJWKS := newTestJWKS(t)
	jwksServer := startJWKSServer(t, externalJWKS)
	claims := externalClaims()
	claims.Audience = jwt.Audience{testExternalAudience, testIssuer}
	subjectToken := externalJWKS.signToken(t, claims, map[string]any{"azp": "trusted-app"})

	validator := newMultiValidator(t, selfJWKS, []TrustedIssuer{{
		IssuerURL:              testExternalIssuer,
		ExpectedAudience:       testExternalAudience,
		JWKSURL:                jwksServer.URL + "/jwks",
		ActorMatcher:           `claims.azp == "trusted-app"`,
		AllowedDelegateClients: []string{testAgentClientID},
	}})
	h := newTestHandlerWithValidator(validator)
	h.allowedAudiences = []string{testIssuer, testAPIAudience}
	h.configuredDelegateClients = []string{testAgentClientID}
	h.clientPolicies = map[string]ConfiguredClientPolicy{testAgentClientID: {APIAudience: testAPIAudience}}

	client := defaultClient()
	client.Audience = []string{testIssuer, testAPIAudience}
	req := newAccessRequest(t, client, url.Values{
		"grant_type":         {oauthproto.GrantTypeTokenExchange},
		"subject_token":      {subjectToken},
		"subject_token_type": {oauthproto.TokenTypeAccessToken},
		"resource":           {testAPIAudience},
	})
	requireHint(t, h.HandleTokenEndpointRequest(context.Background(), req), "not covered by the subject token")
}

// TestTokenExchangeHandler_APIAudienceNotForReExchangedExternalTokens pins that
// a self-issued token carrying an external_issuer claim, as a re-exchange of an
// external token does, loses the api_audience exception.
func TestTokenExchangeHandler_APIAudienceNotForReExchangedExternalTokens(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	tests := []struct {
		name           string
		externalIssuer string
		wantErr        bool
	}{
		{name: "derived from an external token", externalIssuer: testExternalIssuer, wantErr: true},
		{name: "not derived from an external token"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			extra := validExtraClaims()
			if tt.externalIssuer != "" {
				extra["external_issuer"] = tt.externalIssuer
			}
			h := sourcePolicyHandler(t, tj, &ConfiguredClientPolicy{APIAudience: testAPIAudience})
			client := defaultClient()
			client.Audience = []string{testIssuer, testAPIAudience}
			req := newAccessRequest(t, client, url.Values{
				"grant_type":         {oauthproto.GrantTypeTokenExchange},
				"subject_token":      {tj.signToken(t, validClaims(), extra)},
				"subject_token_type": {oauthproto.TokenTypeAccessToken},
				"resource":           {testAPIAudience},
			})

			err := h.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr {
				requireHint(t, err, "not covered by the subject token")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, req.GetGrantedAudience(), testAPIAudience)
		})
	}
}

func TestTokenExchangeHandler_APIAudience(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	const uncoveredAudience = "https://other.example.com"

	tests := []struct {
		name     string
		policy   *ConfiguredClientPolicy
		resource string
		wantErr  bool
	}{
		{
			name:   "api audience granted without subject coverage",
			policy: &ConfiguredClientPolicy{APIAudience: testAPIAudience}, resource: testAPIAudience,
		},
		{
			name:   "another uncovered audience refused",
			policy: &ConfiguredClientPolicy{APIAudience: testAPIAudience}, resource: uncoveredAudience, wantErr: true,
		},
		{
			name:   "uncovered audience refused without an api audience",
			policy: &ConfiguredClientPolicy{}, resource: testAPIAudience, wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := sourcePolicyHandler(t, tj, tt.policy)
			h.allowedAudiences = append(h.allowedAudiences, uncoveredAudience)
			client := defaultClient()
			client.Audience = []string{testIssuer, testAPIAudience, uncoveredAudience}

			form := defaultFormValues(t, tj)
			form.Set("resource", tt.resource)
			req := newAccessRequest(t, client, form)

			err := h.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErr {
				requireHint(t, err, "not covered by the subject token")
				return
			}
			require.NoError(t, err)
			assert.Contains(t, req.GetGrantedAudience(), tt.resource)
		})
	}
}
