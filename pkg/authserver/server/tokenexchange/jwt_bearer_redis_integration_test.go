// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/ory/fosite"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// TestJWTBearerHandler_RedisBacked drives the full JWT-bearer grant —
// HandleTokenEndpointRequest then PopulateTokenEndpointResponse — against a
// real *storage.RedisStorage (backed by miniredis) rather than a mock. This
// is the exact path that used to panic: the grant skips client
// authentication, so fosite never attaches a client to the access request,
// and storage.RedisStorage.CreateAccessTokenSession's marshalRequester used
// to dereference that nil client unconditionally. Round-tripping through
// GetAccessTokenSession also proves the synthetic client is retrievable
// without ever being registered via RegisterClient.
func TestJWTBearerHandler_RedisBacked(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	stor := storage.NewRedisStorageWithClient(redisClient, "test:jwt-bearer:")
	t.Cleanup(func() { _ = stor.Close() })

	const (
		issuer   = "https://idp.example.com"
		subject  = "ext-user-456"
		resource = "https://mcp.example.com"
	)
	validator := &testJWTBearerAssertionValidator{
		err: nil,
	}
	resolvedIssuers, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
		IssuerURL:              issuer,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge: time.Hour.String(),
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: subject, AllowedResources: []string{resource}},
			},
		},
	}})
	require.NoError(t, err)

	deps := testIssuanceDeps(validator, resolvedIssuers)
	deps.consumer = stor
	deps.tokenStorage = stor
	handler := newTestJWTBearerHandler(t, deps)

	// The stub validator ignores the raw assertion and always returns these
	// claims, so the JWT content itself doesn't need to be a real signed
	// token for this storage-focused test.
	now := time.Now()
	validator.claims = &ValidatedClaims{
		Issuer:   issuer,
		Subject:  subject,
		JWTID:    "jti-redis-test",
		IssuedAt: now,
		Expiry:   now.Add(30 * time.Minute),
	}

	// The stub validator ignores the raw assertion's claims entirely, but
	// validateAssertionType still parses it as a real signed JWT before the
	// stub even runs, so it must be syntactically valid.
	tj := newTestJWKS(t)
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
	req.Form = map[string][]string{
		"assertion": {signAssertionWithType(t, tj, nil)},
		"resource":  {resource},
	}

	ctx := context.Background()
	require.NoError(t, handler.HandleTokenEndpointRequest(ctx, req))

	responder := fosite.NewAccessResponse()
	require.NoError(t, handler.PopulateTokenEndpointResponse(ctx, req, responder))

	// mockAccessTokenStrategy always returns the fixed signature
	// "test-signature" from GenerateAccessToken — that's the exact value
	// IssueAccessToken passed to CreateAccessTokenSession above.
	retrieved, err := stor.GetAccessTokenSession(ctx, "test-signature", nil)
	require.NoError(t, err)
	require.NotNil(t, retrieved.GetClient(), "synthetic client must survive Redis serialization")
	require.True(t, storage.IsSyntheticClientID(retrieved.GetClient().GetID()))
}

// TestIDJAGHandler_RedisBackedDetails pins that the issued token's
// authorization_details claim survives a Redis round trip unchanged: a
// session read back holds the same raw bytes, so an integer above 2^53 keeps
// every digit.
func TestIDJAGHandler_RedisBackedDetails(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	stor := storage.NewRedisStorageWithClient(redisClient, "test:id-jag:")
	t.Cleanup(func() { _ = stor.Close() })

	deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: detailsClaims(t, "["+bigEntry+"]")},
		idJAGResolvedIssuers(t))
	deps.consumer = stor
	deps.tokenStorage = stor
	handler := newTestIDJAGHandler(t, deps)
	handler.detailsRegistry = newCallDetailsRegistry(t, singleEntryHook(nil))

	ctx := context.Background()
	req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	require.NoError(t, stor.RegisterClient(ctx, req.Client))
	require.NoError(t, handler.HandleTokenEndpointRequest(ctx, req))
	require.NoError(t, handler.PopulateTokenEndpointResponse(ctx, req, fosite.NewAccessResponse()))

	retrieved, err := stor.GetAccessTokenSession(ctx, "test-signature", &session.Session{})
	require.NoError(t, err)
	stored, ok := retrieved.GetSession().(*session.Session)
	require.True(t, ok)
	details, ok := stored.JWTClaims.Extra[authorizationDetailsParam].(json.RawMessage)
	require.True(t, ok, "a stored session holds the claim as raw JSON")
	assert.Equal(t, `[{"type":"`+testCallType+`","note":"big","limit":9007199254740993}]`, string(details))
}
