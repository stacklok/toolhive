// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// readSingleAuditEvent reads the audit log file at path and decodes exactly
// one newline-delimited JSON event, failing the test if there isn't exactly one.
func readSingleAuditEvent(t *testing.T, path string) map[string]any {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotEmpty(t, strings.TrimSpace(string(data)), "audit log is empty; expected one event")

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 1, "expected exactly one audit event, got: %q", string(data))

	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &event))
	return event
}

// signTestJWT signs a JWT with key using the standard test issuer/audience and
// a 30-minute expiry, embedding subject and any extraClaims (e.g. "client_id"
// for a delegation act claim). extraClaims may be nil.
func signTestJWT(t *testing.T, key *rsa.PrivateKey, subject string, extraClaims map[string]any) string {
	t.Helper()

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	require.NoError(t, err)

	now := time.Now()
	builder := jwt.Signed(signer).
		Claims(jwt.Claims{
			Issuer:   testIssuer,
			Subject:  subject,
			Audience: jwt.Audience{testAudience},
			Expiry:   jwt.NewNumericDate(now.Add(30 * time.Minute)),
			IssuedAt: jwt.NewNumericDate(now),
		})
	if extraClaims != nil {
		builder = builder.Claims(extraClaims)
	}
	token, err := builder.Serialize()
	require.NoError(t, err)
	return token
}

// exchangeForDelegatedToken performs an RFC 8693 token exchange against
// serverURL using the given confidential client credentials and subject
// token, and returns the resulting delegated access token.
func exchangeForDelegatedToken(t *testing.T, serverURL, clientID, clientSecret, subjectToken string) string {
	t.Helper()

	resp := makeTokenRequest(t, serverURL, url.Values{
		"grant_type":         {oauthproto.GrantTypeTokenExchange},
		"subject_token":      {subjectToken},
		"subject_token_type": {oauthproto.TokenTypeAccessToken},
		"client_id":          {clientID},
		"client_secret":      {clientSecret},
	})
	defer resp.Body.Close()
	body := parseTokenResponse(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"token exchange should succeed, got %d (body: %v)", resp.StatusCode, body)
	delegated, ok := body["access_token"].(string)
	require.True(t, ok, "access_token should be a string")
	require.NotEmpty(t, delegated)
	return delegated
}

// auditEventForToken runs token through the audit(auth(stub)) middleware
// chain — audit outermost, auth innermost — and returns the single resulting
// audit event. The stub handler does not re-publish the identity itself: this
// pins that TokenValidator.Middleware alone publishes the validated identity
// to the audit holder via the IdentityHolder read-back.
func auditEventForToken(t *testing.T, key *rsa.PrivateKey, token, validationMsg string) map[string]any {
	t.Helper()
	return auditEventForTokenWithConfig(t, key, token, validationMsg, audit.Config{})
}

// auditEventForTokenWithConfig is auditEventForToken for tests that need a
// non-default audit.Config (e.g. MaxDelegationDepth). cfg is taken by value
// and its LogFile field is set on the local copy, so the caller's config is
// never mutated.
func auditEventForTokenWithConfig(
	t *testing.T, key *rsa.PrivateKey, token, validationMsg string, cfg audit.Config,
) map[string]any {
	t.Helper()

	// Build a validator that trusts the server's issuer and keys, exactly like
	// the middleware in front of a protected resource server. The in-process
	// key provider avoids self-referential HTTP JWKS fetches.
	validator, err := auth.NewTokenValidator(context.Background(), auth.TokenValidatorConfig{
		Issuer:   testIssuer,
		Audience: testAudience,
	}, auth.WithKeyProvider(&testKeyProvider{key: key}))
	require.NoError(t, err)

	cfg.LogFile = filepath.Join(t.TempDir(), "audit.log")
	auditor, err := audit.NewAuditorWithTransport(&cfg, "streamable-http")
	require.NoError(t, err)
	t.Cleanup(func() {
		assert.NoError(t, auditor.Close())
	})

	stub := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	auditor.Middleware(validator.Middleware(stub)).ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, validationMsg)

	return readSingleAuditEvent(t, cfg.LogFile)
}

// TestIntegration_TokenExchange_AuditDelegationChain proves the end-to-end
// audit story for RFC 8693 delegation: a delegated token minted by the real
// token-exchange handler is validated by the auth middleware, and the audit
// middleware emits an event whose delegation chain records the acting agent.
func TestIntegration_TokenExchange_AuditDelegationChain(t *testing.T) {
	t.Parallel()

	const (
		agentClientID     = "test-audit-agent-client"
		agentClientSecret = "test-audit-agent-secret"
		delegatedUserSub  = "audit-delegated-user-sub"
	)

	agentClient, err := registration.New(registration.Config{
		ID:                      agentClientID,
		Secret:                  agentClientSecret,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretPost,
		GrantTypes:              []string{oauthproto.GrantTypeTokenExchange},
		Scopes:                  registration.DefaultScopes,
		Audience:                []string{testAudience},
	})
	require.NoError(t, err)

	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m, withExtraClient(agentClient))

	// Mint the subject token (user) signed by the server's key.
	subjectToken := signTestJWT(t, ts.PrivateKey, delegatedUserSub, map[string]any{
		"client_id": agentClientID,
	})

	// Exchange it for a delegated token carrying act.sub = agent.
	delegated := exchangeForDelegatedToken(t, ts.Server.URL, agentClientID, agentClientSecret, subjectToken)

	// Chain: audit (outer) -> auth -> bare stub handler.
	event := auditEventForToken(t, ts.PrivateKey, delegated,
		"delegated token must validate through the auth middleware")

	subjects, ok := event["subjects"].(map[string]any)
	require.True(t, ok, "subjects should be a map")
	assert.Equal(t, delegatedUserSub, subjects["user_id"],
		"audit subject must be the delegated user, not the agent")

	chain, ok := event["delegation"].(map[string]any)
	require.True(t, ok, "delegation must be present in the audit event")
	assert.Equal(t, false, chain["truncated"])
	assert.Equal(t, float64(0), chain["omitted"])
	assert.Equal(t, false, chain["malformed"])
	hops, ok := chain["chain"].([]any)
	require.True(t, ok, "chain should be an array")
	require.Len(t, hops, 1, "a single exchange yields a single hop")
	hop, ok := hops[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, agentClientID, hop["sub"],
		"the delegation chain must record the acting agent client")
}

// TestIntegration_AuditMiddleware_NonDelegatedTokenOmitsDelegationChain is the
// negative companion to TestIntegration_TokenExchange_AuditDelegationChain: a
// plain (non-delegated) token through the same audit(auth(stub)) chain must
// produce an audit event with NO delegation member.
func TestIntegration_AuditMiddleware_NonDelegatedTokenOmitsDelegationChain(t *testing.T) {
	t.Parallel()

	const plainUserSub = "audit-plain-user-sub"

	m := startMockOIDC(t)
	ts := setupTestServerWithMockOIDC(t, m)

	// Mint a plain subject token signed by the server's key (no act claim).
	plainToken := signTestJWT(t, ts.PrivateKey, plainUserSub, nil)

	event := auditEventForToken(t, ts.PrivateKey, plainToken,
		"plain token must validate through the auth middleware")

	subjects, ok := event["subjects"].(map[string]any)
	require.True(t, ok, "subjects should be a map")
	assert.Equal(t, plainUserSub, subjects["user_id"],
		"audit subject must be the authenticated user")

	_, exists := event["delegation"]
	assert.False(t, exists,
		"a non-delegated token must not produce a delegation member")
}

// mintTwoHopDelegatedToken produces a token whose second hop is genuine
// production output of a real RFC 8693 token-exchange call — firstAgentClientID
// "delegates" to secondAgentClientID — while the first hop is a hand-signed
// stand-in for an earlier exchange this test never performs. What this pins
// is the handler's nesting direction: the second (real) actor must land at
// chain[0], outermost, with the hand-signed first actor at chain[1].
//
// This AS can honour an RFC 8693 §4.4 may_act claim — checkDelegationConsent
// (handler.go) treats it as the authoritative consent signal and skips the
// client_id binding entirely when present — but no code path anywhere writes
// may_act, and the exchange does not propagate an inbound one:
// pkg/authserver/server/tokenexchange/validator.go:276-281 routes may_act
// into the structured MayAct field so it never lands in Extra, and
// handler.go:160 only ever copies Extra["act"]. So the subject token here
// hand-signs the one claim a may_act-emitting issuer would have written
// (plus a prior "act" entry standing in for that earlier hop); everything
// downstream of it — the consent check, the act-claim nesting, the issued
// token — is genuine production output of a single real exchange.
func mintTwoHopDelegatedToken(t *testing.T) (ts *testServerWithUpstream, delegated, firstAgentClientID, secondAgentClientID string) {
	t.Helper()

	const (
		firstAgent        = "test-first-agent-client"
		secondAgent       = "test-second-agent-client"
		secondAgentSecret = "test-second-agent-secret" //nolint:gosec // test fixture, not a real credential
		delegatedUserSub  = "audit-multi-actor-user-sub"
	)

	secondAgentClient, err := registration.New(registration.Config{
		ID:                      secondAgent,
		Secret:                  secondAgentSecret,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodClientSecretPost,
		GrantTypes:              []string{oauthproto.GrantTypeTokenExchange},
		Scopes:                  registration.DefaultScopes,
		Audience:                []string{testAudience},
	})
	require.NoError(t, err)

	m := startMockOIDC(t)
	ts = setupTestServerWithMockOIDC(t, m, withExtraClient(secondAgentClient))

	subjectToken := signTestJWT(t, ts.PrivateKey, delegatedUserSub, map[string]any{
		"client_id": firstAgent,                         // ignored: may_act wins
		"act":       map[string]any{"sub": firstAgent},  // prior hop
		"may_act":   map[string]any{"sub": secondAgent}, // RFC 8693 §4.4 consent
	})

	delegated = exchangeForDelegatedToken(t, ts.Server.URL, secondAgent, secondAgentSecret, subjectToken)
	return ts, delegated, firstAgent, secondAgent
}

// TestIntegration_TokenExchange_MultiActorDelegationChain proves the
// handler's nesting direction end to end: hop 2 (secondAgentClientID) is
// real handler output from an actual token-exchange call, hop 1
// (firstAgentClientID) is the hand-signed stand-in described on
// mintTwoHopDelegatedToken, and the audit event for the resulting token
// orders the two hops outermost (most recent) first.
func TestIntegration_TokenExchange_MultiActorDelegationChain(t *testing.T) {
	t.Parallel()

	ts, delegated, firstAgentClientID, secondAgentClientID := mintTwoHopDelegatedToken(t)

	event := auditEventForToken(t, ts.PrivateKey, delegated,
		"re-delegated token must validate through the auth middleware")

	chain, ok := event["delegation"].(map[string]any)
	require.True(t, ok, "delegation must be present in the audit event")
	assert.Equal(t, false, chain["truncated"])
	hops, ok := chain["chain"].([]any)
	require.True(t, ok, "chain should be an array")
	require.Len(t, hops, 2, "a re-delegated token yields two hops")

	hop0, ok := hops[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, secondAgentClientID, hop0["sub"],
		"chain[0] (outermost) must be the client that performed the second exchange")
	hop1, ok := hops[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, firstAgentClientID, hop1["sub"],
		"chain[1] (innermost) must be the earlier, first-hop actor")
}

// TestIntegration_TokenExchange_TruncatedDelegationChainAuditEvent proves
// that an auditor configured with MaxDelegationDepth: 1 truncates a genuine
// 2-hop delegation chain down to its outermost (most recent) actor.
func TestIntegration_TokenExchange_TruncatedDelegationChainAuditEvent(t *testing.T) {
	t.Parallel()

	ts, delegated, _, secondAgentClientID := mintTwoHopDelegatedToken(t)

	maxDepth := 1
	event := auditEventForTokenWithConfig(t, ts.PrivateKey, delegated,
		"re-delegated token must validate through the auth middleware",
		audit.Config{MaxDelegationDepth: &maxDepth})

	chain, ok := event["delegation"].(map[string]any)
	require.True(t, ok, "delegation must be present in the audit event")
	assert.Equal(t, true, chain["truncated"])
	assert.Equal(t, float64(1), chain["omitted"],
		"one of the two hops must be reported omitted")

	hops, ok := chain["chain"].([]any)
	require.True(t, ok, "chain should be an array")
	require.Len(t, hops, 1, "MaxDelegationDepth=1 must leave exactly one surviving hop")
	hop0, ok := hops[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, secondAgentClientID, hop0["sub"],
		"the surviving hop must be the outermost one")
}
