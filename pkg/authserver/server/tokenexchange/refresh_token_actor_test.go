// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	issueProfileClient = "handler-actor-client"
	issuePlainClient   = "handler-plain-client"
	issueTarget        = "https://as2.example.com"
	issueResource      = "https://gw.example.com/mcp"
	issueOtherResource = "https://gw.example.com/other"
	issueActorToken    = "eyJ.handler-actor-token.sig"
	issueSubIDValue    = "handler-sub-id"
	issueUpstreamValue = "handler-upstream-value"
	issueSubID         = `{"format":"iss_sub","iss":"https://idp.example","sub":"` + issueSubIDValue + `"}`
	issueUpstream      = `{"iss":"https://idp.example","claim":"oid","value":"` + issueUpstreamValue + `"}`
	// issueBound bounds every blocking wait in these tests.
	issueBound = 5 * time.Second
)

var (
	issueActorFailure = fosite.ErrServerError.WithHint("The actor token could not be validated.").GetDescription()
	issueMintFailure  = fosite.ErrServerError.WithHint("The ID-JAG could not be minted.").GetDescription()
)

// issuePolicies configures two clients with the same ID-JAG target: one with
// an actor profile whose validator is v, one without a profile. required lists
// the target's required subject claims.
func issuePolicies(v tokenexchange.ActorTokenValidator, required ...string) map[string]tokenexchange.ConfiguredClientPolicy {
	targets := map[string]tokenexchange.IDJAGTarget{issueTarget: {
		Issuer: issueTarget, ClientID: "broker-at-as2", Resources: []string{issueResource, issueOtherResource},
		Lifetime: time.Minute, RequiredSubjectClaims: required,
	}}
	exchange := map[string]time.Duration{
		oauthproto.TokenTypeIDJAG: 12 * time.Hour, oauthproto.TokenTypeRefreshToken: 720 * time.Hour,
	}
	return map[string]tokenexchange.ConfiguredClientPolicy{
		issueProfileClient: {ExchangeRefreshTokens: exchange, IDJAGTargets: targets, ActorProfile: &tokenexchange.ActorProfile{
			Name: "handler-validator", ActorTokenType: oauthproto.TokenTypeJWT,
			ActorValidatorRegistration: tokenexchange.ActorValidatorRegistration{
				Validator: v, ActIssuer: "https://agents.example.com",
			},
		}},
		issuePlainClient: {ExchangeRefreshTokens: exchange, IDJAGTargets: targets},
	}
}

// issueGrant is a live ID-JAG grant with the given signature, owned by client,
// recording subID and upstream ("" for none).
func issueGrant(sig, client, subID, upstream string) storage.ExchangeGrant {
	return storage.ExchangeGrant{
		Signature: sig, ClientID: client, Subject: "user-1",
		SubjectIdentifier: subID, UpstreamSubject: upstream,
		IssuableTokenTypes: []string{oauthproto.TokenTypeIDJAG},
		ExpiresAt:          time.Now().Add(time.Hour), CreatedAt: time.Now(),
	}
}

// issueGrants keys grants by signature. Unless extra holds a grant with the
// client's ID as its signature, each client gets one that records the test
// user's sub_id and upstream_subject.
func issueGrants(extra ...storage.ExchangeGrant) map[string]storage.ExchangeGrant {
	out := map[string]storage.ExchangeGrant{
		issueProfileClient: issueGrant(issueProfileClient, issueProfileClient, issueSubID, issueUpstream),
		issuePlainClient:   issueGrant(issuePlainClient, issuePlainClient, issueSubID, issueUpstream),
	}
	for _, g := range extra {
		out[g.Signature] = g
	}
	return out
}

// issueRequest asks client for an ID-JAG from the grant with signature sig.
// The profile client presents the actor token; the plain client presents none.
func issueRequest(client, sig string) *fosite.AccessRequest {
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeTokenExchange}
	req.Client = &fosite.DefaultClient{ID: client, GrantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange}}
	req.Form = url.Values{
		"grant_type":           {oauthproto.GrantTypeTokenExchange},
		"subject_token":        {"rt." + sig},
		"subject_token_type":   {oauthproto.TokenTypeRefreshToken},
		"requested_token_type": {oauthproto.TokenTypeIDJAG},
		"audience":             {issueTarget},
		"resource":             {issueResource},
	}
	if client == issueProfileClient {
		req.Form.Set("actor_token", issueActorToken)
		req.Form.Set("actor_token_type", oauthproto.TokenTypeJWT)
	}
	return req
}

// issueBothPhases runs the request through both handler phases, as fosite
// does, and returns the response and the first error.
func issueBothPhases(ctx context.Context, h *tokenexchange.RefreshTokenHandler, req *fosite.AccessRequest) (
	*fosite.AccessResponse, error,
) {
	resp := fosite.NewAccessResponse()
	if err := h.HandleTokenEndpointRequest(ctx, req); err != nil {
		return resp, err
	}
	return resp, h.PopulateTokenEndpointResponse(ctx, req, resp)
}

// requireIssueError checks the status, error and error_description err sends,
// and returns the fosite error.
func requireIssueError(t *testing.T, err error, code int, errorField, description string) *fosite.RFC6749Error {
	t.Helper()
	var rfc *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfc)
	assert.Equal(t, errorField, rfc.ErrorField)
	assert.Equal(t, code, rfc.CodeField)
	assert.Equal(t, description, rfc.GetDescription())
	return rfc
}

// aboveDebug returns the captured lines for client at Info or above.
func aboveDebug(logs *tokenexchange.LogBufferForTest, client string) []string {
	var out []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "client_id="+client+" ") && !strings.Contains(line, "level=DEBUG") {
			out = append(out, line)
		}
	}
	return out
}

// assertOneRefusal asserts that the request logged exactly one line above
// Debug: the Warn refusal with reason, carrying cause ("" for none), and no
// actor token, sub_id or upstream identity.
func assertOneRefusal(t *testing.T, logs *tokenexchange.LogBufferForTest, client, reason, cause string) {
	t.Helper()
	lines := aboveDebug(logs, client)
	require.Len(t, lines, 1, "one refusal line per request")
	assert.Contains(t, lines[0], "level=WARN")
	assert.Contains(t, lines[0], `msg="Exchange grant refused"`)
	assert.Contains(t, lines[0], "reason="+reason)
	if cause == "" {
		assert.NotContains(t, lines[0], "error=")
	} else {
		assert.Contains(t, lines[0], cause)
	}
	assertNoIssueSecrets(t, lines[0])
}

func assertNoIssueSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range []string{issueActorToken, issueSubIDValue, issueUpstreamValue} {
		assert.NotContains(t, text, secret)
	}
}

// TestIDJAGActorValidationRequest pins what the validator receives: the
// checked actor token and type, the client, the grant's user as recorded and
// the configured target with the requested resource, whatever else the
// request carries. It is called exactly once, in the second handler phase.
func TestIDJAGActorValidationRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		grants    map[string]storage.ExchangeGrant
		presented string
		wantGrant tokenexchange.GrantOwner
	}{
		{name: "a recorded identity", grants: issueGrants(), presented: issueProfileClient,
			wantGrant: tokenexchange.GrantOwner{Subject: "user-1", SubjectIdentifier: issueSubID,
				Upstream: tokenexchange.UpstreamIdentity{Issuer: "https://idp.example", Subject: issueSubIDValue,
					StableClaim: "oid", StableValue: issueUpstreamValue}}},
		{name: "no recorded identity is the zero value", presented: "bare",
			grants:    issueGrants(issueGrant("bare", issueProfileClient, "", "")),
			wantGrant: tokenexchange.GrantOwner{Subject: "user-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			calls := 0
			var got tokenexchange.ActorRequest
			v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
				func(_ context.Context, req tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
					calls++
					got = req
					return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: time.Now().Add(time.Hour)}, nil
				}).Times(1)
			h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v), tc.grants)

			req := issueRequest(issueProfileClient, tc.presented)
			req.Form.Set("resource", issueOtherResource)
			req.Form.Set("sub", "attacker")
			req.Form.Set("sub_id", `{"format":"iss_sub","iss":"https://evil.example","sub":"attacker"}`)
			require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
			assert.Zero(t, calls, "the first phase never calls the validator")
			resp := fosite.NewAccessResponse()
			require.NoError(t, h.PopulateTokenEndpointResponse(t.Context(), req, resp))
			assert.NotEmpty(t, resp.GetAccessToken())

			assert.Equal(t, tokenexchange.ActorRequest{
				ActorToken:     issueActorToken,
				ActorTokenType: oauthproto.TokenTypeJWT,
				ClientID:       issueProfileClient,
				Grant:          tc.wantGrant,
				Target:         tokenexchange.ActorTarget{Issuer: issueTarget, Resource: issueOtherResource},
			}, got)
		})
	}
}

// TestIDJAGActorValidationOutcomes pins how the validator's answer reaches the
// client and the log: one Warn line per refusal, with the cause redacted, and
// the actor token in no line at any level. A validator that outlasts the
// validation budget is a server error.
//
//nolint:paralleltest // swaps the process-wide default logger
func TestIDJAGActorValidationOutcomes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	const budget = 100 * time.Millisecond
	returns := func(result tokenexchange.ActorResult, err error) func(
		context.Context, tokenexchange.ActorRequest,
	) (tokenexchange.ActorResult, error) {
		return func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			return result, err
		}
	}
	tests := []struct {
		name       string
		validate   func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error)
		opts       []tokenexchange.RefreshTokenOption
		wantCode   int
		wantError  string
		wantDesc   string
		wantReason string
		wantCause  string
	}{
		{name: "a denial passes through",
			validate: returns(tokenexchange.ActorResult{}, fmt.Errorf("agent presenting %s: %w", issueActorToken,
				&tokenexchange.ActorDeniedError{Reason: tokenexchange.ActorDeniedReasonApprovalRequired})),
			wantCode: http.StatusBadRequest, wantError: "invalid_request", wantDesc: "approval_required",
			wantReason: "approval_required", wantCause: "agent presenting [redacted]: actor denied: approval_required"},
		{name: "a plain error is a server error",
			validate: returns(tokenexchange.ActorResult{}, fmt.Errorf("directory unreachable for %s", issueActorToken)),
			wantCode: http.StatusInternalServerError, wantError: "server_error", wantDesc: issueActorFailure,
			wantReason: "actor_validator_error", wantCause: "directory unreachable for [redacted]"},
		{name: "an expiry in the past is actor_token_expired",
			validate: returns(tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: now.Add(-5 * time.Second)}, nil),
			wantCode: http.StatusBadRequest, wantError: "invalid_request", wantDesc: "actor_token_expired",
			wantReason: "actor_token_expired"},
		{name: "a validator that outlasts the budget is a server error",
			validate: func(ctx context.Context, _ tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
				select {
				case <-ctx.Done():
					return tokenexchange.ActorResult{}, ctx.Err()
				case <-time.After(issueBound):
					return tokenexchange.ActorResult{}, fmt.Errorf("the validator context was never canceled")
				}
			},
			opts:     []tokenexchange.RefreshTokenOption{tokenexchange.WithActorValidationTimeoutForTest(budget)},
			wantCode: http.StatusInternalServerError, wantError: "server_error", wantDesc: issueActorFailure,
			wantReason: "actor_validator_timeout", wantCause: fmt.Sprintf("validator did not return within %s", budget)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logs := tokenexchange.CaptureLogsForTest(t)
			v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(tc.validate).Times(1)
			h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v), issueGrants(), append(
				[]tokenexchange.RefreshTokenOption{tokenexchange.WithClockForTest(func() time.Time { return now })},
				tc.opts...)...)

			resp, err := issueBothPhases(t.Context(), h, issueRequest(issueProfileClient, issueProfileClient))
			rfc := requireIssueError(t, err, tc.wantCode, tc.wantError, tc.wantDesc)
			assert.Empty(t, rfc.DebugField, "validator text never goes to the debug field")
			assert.Empty(t, resp.GetAccessToken())
			assertOneRefusal(t, logs, issueProfileClient, tc.wantReason, tc.wantCause)
			assert.NotContains(t, logs.String(), issueActorToken)
		})
	}
}

// TestIDJAGActorValidationUsesTheHandlerClock pins that both handler phases
// and the validator's answer are checked at the handler's clock: with a clock
// two hours back, a grant that ended an hour ago and an answer that ended half
// an hour ago are still live, and the request mints.
func TestIDJAGActorValidationUsesTheHandlerClock(t *testing.T) {
	t.Parallel()

	grant := issueGrant(issueProfileClient, issueProfileClient, issueSubID, issueUpstream)
	grant.ExpiresAt = time.Now().Add(-time.Hour)
	v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).
		Return(tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: time.Now().Add(-30 * time.Minute)}, nil).Times(1)
	past := time.Now().Add(-2 * time.Hour)
	h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v),
		map[string]storage.ExchangeGrant{issueProfileClient: grant},
		tokenexchange.WithClockForTest(func() time.Time { return past }))

	resp, err := issueBothPhases(t.Context(), h, issueRequest(issueProfileClient, issueProfileClient))
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetAccessToken())
}

// TestIDJAGActorUpstreamIdentityFault pins that a grant whose recorded upstream
// identity is incomplete is a server error for a client with an actor profile,
// decided before the validator. The cause names the claim, never its value,
// and travels in the debug field. A client without a profile mints from the
// same grant.
//
//nolint:paralleltest // swaps the process-wide default logger
func TestIDJAGActorUpstreamIdentityFault(t *testing.T) {
	logs := tokenexchange.CaptureLogsForTest(t)
	// No expectation: a call to the validator fails the test.
	v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	grants := map[string]storage.ExchangeGrant{
		issueProfileClient: issueGrant(issueProfileClient, issueProfileClient, issueSubID, ""),
		issuePlainClient:   issueGrant(issuePlainClient, issuePlainClient, issueSubID, ""),
	}
	h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v), grants)

	_, err := issueBothPhases(t.Context(), h, issueRequest(issueProfileClient, issueProfileClient))
	rfc := requireIssueError(t, err, http.StatusInternalServerError, "server_error", issueMintFailure)
	assert.Equal(t, "the grant recorded only one of sub_id and upstream_subject", rfc.DebugField)
	assertOneRefusal(t, logs, issueProfileClient, "internal_error", "")

	_, err = issueBothPhases(t.Context(), h, issueRequest(issuePlainClient, issuePlainClient))
	require.NoError(t, err, "a client without a profile does not read the upstream identity")
}

// TestIDJAGActorNotCalledAfterEarlierRefusal pins that an earlier refusal ends
// the request before the validator: the mock has no expectations, so a call
// fails the test.
func TestIDJAGActorNotCalledAfterEarlierRefusal(t *testing.T) {
	t.Parallel()

	revoked := issueGrant("revoked", issueProfileClient, issueSubID, issueUpstream)
	revoked.RevokedAt = time.Now()
	grants := issueGrants(revoked, issueGrant("no-upstream", issueProfileClient, "", ""))

	tests := []struct {
		name     string
		sig      string
		required []string
		mutate   func(url.Values)
		code     string
		desc     string
	}{
		{name: "bad target", sig: issueProfileClient,
			mutate: func(f url.Values) { f.Set("audience", "https://unknown.example") }, code: "invalid_target"},
		{name: "revoked grant", sig: "revoked", code: "invalid_request", desc: "revoked"},
		{name: "missing subject claim", sig: "no-upstream", required: []string{"upstream_subject"},
			code: "invalid_request", desc: "upstream_subject_missing"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
			h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v, tc.required...), grants)
			req := issueRequest(issueProfileClient, tc.sig)
			if tc.mutate != nil {
				tc.mutate(req.Form)
			}
			// Each phase decides on its own, so both refuse.
			for _, err := range []error{
				h.HandleTokenEndpointRequest(t.Context(), req),
				h.PopulateTokenEndpointResponse(t.Context(), req, fosite.NewAccessResponse()),
			} {
				var rfc *fosite.RFC6749Error
				require.ErrorAs(t, err, &rfc)
				assert.Equal(t, tc.code, rfc.ErrorField)
				if tc.desc != "" {
					assert.Equal(t, tc.desc, rfc.GetDescription())
				}
			}
		})
	}
}
