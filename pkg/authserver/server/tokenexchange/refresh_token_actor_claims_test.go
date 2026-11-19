// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
	"github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	claimsIssuerA      = "https://agents-a.example.com"
	claimsIssuerB      = "spiffe://example.org"
	claimsServerIssuer = "https://auth.example.com"
	claimsCredSubject  = "cred-subject-sentinel"
)

// claimsMint runs the request through both phases and returns the minted
// ID-JAG's claims, with times as json.Number, and the response's expires_in.
func claimsMint(t *testing.T, h *tokenexchange.RefreshTokenHandler, client string) (map[string]any, int64) {
	t.Helper()
	req := issueRequest(client, client)
	if client != issuePlainClient {
		req.Form.Set("actor_token", issueActorToken)
		req.Form.Set("actor_token_type", oauthproto.TokenTypeJWT)
	}
	resp, err := issueBothPhases(t.Context(), h, req)
	require.NoError(t, err)
	claims := mintedClaims(t, resp.GetAccessToken())
	expiresIn, ok := resp.ToMap()["expires_in"].(int64)
	require.True(t, ok, "expires_in is an int64: %v", resp.ToMap()["expires_in"])
	return claims, expiresIn
}

// mintedClaims decodes the payload of a minted ID-JAG without checking its
// signature.
func mintedClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, json.Unmarshal(raw, &claims))
	return claims
}

// claimsTime returns the numeric claim name in whole seconds.
func claimsTime(t *testing.T, claims map[string]any, name string) int64 {
	t.Helper()
	v, ok := claims[name].(float64)
	require.True(t, ok, "%s is a number: %v", name, claims[name])
	return int64(v)
}

// claimsValidator is a mock validator that answers with the result of answer
// and is expected to be called exactly calls times.
func claimsValidator(t *testing.T, answer func() tokenexchange.ActorResult, calls int) *mocks.MockActorTokenValidator {
	t.Helper()
	v := mocks.NewMockActorTokenValidator(gomock.NewController(t))
	v.EXPECT().ValidateActor(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, tokenexchange.ActorRequest) (tokenexchange.ActorResult, error) {
			return answer(), nil
		}).Times(calls)
	return v
}

// claimsClock is a handler clock that a validator can move.
type claimsClock struct{ now time.Time }

func (c *claimsClock) read() time.Time { return c.now }

func newClaimsClock() *claimsClock { return &claimsClock{now: time.Now().Truncate(time.Second)} }

// TestIDJAGActClaim pins act for two clients registered with different
// issuers and the same validator answer: act is exactly the client's own
// registered issuer and the agent ID, never the server's issuer and never
// nested.
func TestIDJAGActClaim(t *testing.T) {
	t.Parallel()

	const (
		clientA, clientB = "claims-client-a", "claims-client-b"
		spiffeID         = "spiffe://example.org/ns/a/sa/b"
	)
	v := claimsValidator(t, func() tokenexchange.ActorResult {
		return tokenexchange.ActorResult{AgentID: spiffeID, ExpiresAt: time.Now().Add(time.Hour)}
	}, 2)
	policies := issuePolicies(v)
	for client, issuer := range map[string]string{clientA: claimsIssuerA, clientB: claimsIssuerB} {
		p := policies[issueProfileClient]
		profile := *p.ActorProfile
		profile.ActIssuer = issuer
		p.ActorProfile = &profile
		policies[client] = p
	}
	h := tokenexchange.NewRefreshTokenHandlerForTest(t, policies, map[string]storage.ExchangeGrant{
		clientA: issueGrant(clientA, clientA, issueSubID, issueUpstream),
		clientB: issueGrant(clientB, clientB, issueSubID, issueUpstream),
	})

	for client, issuer := range map[string]string{clientA: claimsIssuerA, clientB: claimsIssuerB} {
		claims, _ := claimsMint(t, h, client)
		assert.Equal(t, map[string]any{"iss": issuer, "sub": spiffeID}, claims["act"], "exactly iss and sub")
		assert.Equal(t, claimsServerIssuer, claims["iss"])
	}
}

// TestIDJAGActorExpiry pins iat and exp where the actor's answer matters: the
// credential's expiry caps exp, iat is the instant after the validator
// returned and the cap counts from it, and a grant that expires while the
// validator runs is refused as invalid_grant, not as a server error.
func TestIDJAGActorExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		grantEnd  time.Duration
		credEnd   time.Duration
		advanceBy time.Duration
		wantIat   int64 // iat - decision time
		wantExpIn int64 // exp - iat
		wantDesc  string
	}{
		{name: "the credential's expiry is earliest", grantEnd: 12 * time.Hour, credEnd: 20*time.Second + 900*time.Millisecond,
			wantExpIn: 20},
		{name: "iat is the instant after the validator returned", grantEnd: 12 * time.Hour, credEnd: time.Hour,
			advanceBy: 7 * time.Second, wantIat: 7, wantExpIn: 60},
		{name: "the credential's cap counts from that later instant", grantEnd: 12 * time.Hour, credEnd: 10 * time.Second,
			advanceBy: 4 * time.Second, wantIat: 4, wantExpIn: 6},
		{name: "a grant that expires during the validator call", grantEnd: 5 * time.Second, credEnd: time.Hour,
			advanceBy: 10 * time.Second, wantDesc: "bound_elapsed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := newClaimsClock()
			start := clock.now
			v := claimsValidator(t, func() tokenexchange.ActorResult {
				clock.now = clock.now.Add(tc.advanceBy)
				return tokenexchange.ActorResult{AgentID: "agent-1", ExpiresAt: start.Add(tc.credEnd)}
			}, 1)
			grant := issueGrant(issueProfileClient, issueProfileClient, issueSubID, issueUpstream)
			grant.ExpiresAt = start.Add(tc.grantEnd)
			h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(v),
				map[string]storage.ExchangeGrant{issueProfileClient: grant}, tokenexchange.WithClockForTest(clock.read))

			if tc.wantDesc != "" {
				resp, err := issueBothPhases(t.Context(), h, issueRequest(issueProfileClient, issueProfileClient))
				requireIssueError(t, err, http.StatusBadRequest, "invalid_grant", tc.wantDesc)
				assert.Empty(t, resp.GetAccessToken())
				return
			}
			claims, expiresIn := claimsMint(t, h, issueProfileClient)
			iat, exp := claimsTime(t, claims, "iat"), claimsTime(t, claims, "exp")
			assert.Equal(t, start.Unix()+tc.wantIat, iat)
			assert.Equal(t, tc.wantExpIn, exp-iat)
			assert.Greater(t, exp, iat)
			assert.Equal(t, exp-iat, expiresIn, "expires_in is exp - iat")
		})
	}
}

// TestIDJAGActorMintLog pins what a successful mint shows and hides. The Debug
// line names the agent and the credential, nothing above Debug is logged, and
// neither the actor token nor the credential's subject enters a claim or the
// line. A client without a profile mints with no act and no actor attributes.
//
//nolint:paralleltest // swaps the process-wide default logger
func TestIDJAGActorMintLog(t *testing.T) {
	logs := tokenexchange.CaptureLogsForTest(t)
	clock := newClaimsClock()
	answer := func() tokenexchange.ActorResult {
		clock.now = clock.now.Add(3 * time.Second)
		return tokenexchange.ActorResult{
			AgentID: "agent-1", ExpiresAt: clock.now.Add(time.Hour), CredentialSubject: claimsCredSubject,
		}
	}
	grants := issueGrants()
	for client, g := range grants {
		g.ExpiresAt = clock.now.Add(24 * time.Hour)
		grants[client] = g
	}
	h := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(claimsValidator(t, answer, 1)), grants,
		tokenexchange.WithClockForTest(clock.read))
	mintedLine := func(client string) []string {
		var out []string
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "client_id="+client+" ") && strings.Contains(line, `msg="ID-JAG minted"`) {
				out = append(out, line)
			}
		}
		return out
	}

	t.Run("a profiled client", func(t *testing.T) {
		claims, _ := claimsMint(t, h, issueProfileClient)
		raw, err := json.Marshal(claims)
		require.NoError(t, err)
		assert.NotContains(t, string(raw), claimsCredSubject)
		assert.NotContains(t, string(raw), issueActorToken)

		minted := mintedLine(issueProfileClient)
		require.Len(t, minted, 1)
		assert.Contains(t, minted[0], "level=DEBUG")
		assert.Contains(t, minted[0], "act_sub=agent-1")
		assert.Contains(t, minted[0], "actor_credential_subject="+claimsCredSubject)
		assert.NotContains(t, minted[0], issueActorToken)
		assert.Empty(t, aboveDebug(logs, issueProfileClient))
	})

	t.Run("a client without a profile", func(t *testing.T) {
		plain := tokenexchange.NewRefreshTokenHandlerForTest(t, issuePolicies(claimsValidator(t, answer, 0)), grants,
			tokenexchange.WithClockForTest(clock.read))
		claims, _ := claimsMint(t, plain, issuePlainClient)
		assert.NotContains(t, claims, "act")

		minted := mintedLine(issuePlainClient)
		require.Len(t, minted, 1)
		assert.NotContains(t, minted[0], "act_sub")
		assert.NotContains(t, minted[0], "actor_credential_subject")
	})
}
