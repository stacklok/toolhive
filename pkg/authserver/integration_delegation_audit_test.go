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

	// Build a validator that trusts the server's issuer and keys, exactly like
	// the middleware in front of a protected resource server. The in-process
	// key provider avoids self-referential HTTP JWKS fetches.
	validator, err := auth.NewTokenValidator(context.Background(), auth.TokenValidatorConfig{
		Issuer:   testIssuer,
		Audience: testAudience,
	}, auth.WithKeyProvider(&testKeyProvider{key: key}))
	require.NoError(t, err)

	auditLog := filepath.Join(t.TempDir(), "audit.log")
	auditor, err := audit.NewAuditorWithTransport(&audit.Config{LogFile: auditLog}, "streamable-http")
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

	return readSingleAuditEvent(t, auditLog)
}

// TestIntegration_TokenExchange_AuditDelegationChain proves the end-to-end
// audit story for RFC 8693 delegation: a delegated token minted by the real
// token-exchange handler is validated by the auth middleware, and the audit
// middleware emits an event whose delegationChain records the acting agent.
func TestIntegration_TokenExchange_AuditDelegationChain(t *testing.T) {
	t.Parallel()

	const (
		agentClientID     = "test-audit-agent-client"
		agentClientSecret = "test-audit-agent-secret"
		delegatedUserSub  = "audit-delegated-user-sub"
	)

	agentClient, err := registration.New(registration.Config{
		ID:         agentClientID,
		Secret:     agentClientSecret,
		Public:     false,
		GrantTypes: []string{oauthproto.GrantTypeTokenExchange},
		Scopes:     registration.DefaultScopes,
		Audience:   []string{testAudience},
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

	chain, ok := event["delegation_chain"].(map[string]any)
	require.True(t, ok, "delegation_chain must be present in the audit event")
	assert.Equal(t, false, chain["truncated"])
	actors, ok := chain["actors"].([]any)
	require.True(t, ok, "actors should be an array")
	require.Len(t, actors, 1, "a single exchange yields a single actor")
	actor, ok := actors[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, agentClientID, actor["sub"],
		"the delegation chain must record the acting agent client")
}

// TestIntegration_AuditMiddleware_NonDelegatedTokenOmitsDelegationChain is the
// negative companion to TestIntegration_TokenExchange_AuditDelegationChain: a
// plain (non-delegated) token through the same audit(auth(stub)) chain must
// produce an audit event with NO delegation_chain member.
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

	_, exists := event["delegation_chain"]
	assert.False(t, exists,
		"a non-delegated token must not produce a delegation_chain member")
}
