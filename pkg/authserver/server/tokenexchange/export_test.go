// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// ValidateActorWithLimiterForTest exposes validateActor to the external test package, which
// needs the generated mock package that itself imports tokenexchange.
var ValidateActorWithLimiterForTest = validateActor

// NewActorLimiterForTest exposes newActorLimiter. The limiter type is unexported;
// tests only pass the value on.
var NewActorLimiterForTest = newActorLimiter

// ActorLimiterInUseForTest reports how many slots of l are held.
func ActorLimiterInUseForTest(l *actorLimiter) int { return len(l.slots) }

// AcquireActorLimiterForTest calls l.acquire.
func AcquireActorLimiterForTest(ctx context.Context, l *actorLimiter) error { return l.acquire(ctx) }

// DefaultActorValidatorCapacityForTest is the production limiter capacity.
const DefaultActorValidatorCapacityForTest = defaultActorValidatorCapacity

// ValidateActorForTest calls validateActor with a limiter of the default capacity
// that is private to the call, for tests that are not about the limiter.
func ValidateActorForTest(
	ctx context.Context,
	v ActorTokenValidator,
	req ActorRequest,
	clock func() time.Time,
	timeout time.Duration,
) (ActorResult, time.Time, string, error) {
	limiter, err := newActorLimiter(defaultActorValidatorCapacity)
	if err != nil {
		return ActorResult{}, time.Time{}, "", err
	}
	return validateActor(ctx, v, limiter, req, clock, timeout)
}

// ActorValidationTimeoutForTest is the production validation budget, so tests
// that are not about the timeout run with the same budget the server uses.
const ActorValidationTimeoutForTest = actorValidationTimeout

// WithActorValidationTimeoutForTest and WithClockForTest expose the
// RefreshTokenFactory options that control actor validation timing.
var (
	WithActorValidationTimeoutForTest = withActorValidationTimeout
	WithClockForTest                  = withClock
)

// LogBufferForTest is the concurrency-safe log sink CaptureLogsForTest returns.
type LogBufferForTest = syncBuffer

// CaptureLogsForTest routes the process-wide default logger to the returned
// buffer until the test ends. Callers must not run in parallel.
var CaptureLogsForTest = captureLogs

// NewRefreshTokenHandlerForTest builds a RefreshTokenHandler through
// RefreshTokenFactory with a test signer, the access-token issuer
// "https://auth.example.com" and a store that serves grants keyed by
// signature. A presented refresh token's signature is the text after its last
// dot, and a token that starts with "bad" fails its HMAC check.
func NewRefreshTokenHandlerForTest(
	t *testing.T, policies map[string]ConfiguredClientPolicy, grants map[string]storage.ExchangeGrant,
	options ...RefreshTokenOption,
) *RefreshTokenHandler {
	t.Helper()
	return buildRefreshTokenHandler(t, policies, &grantsBySignature{grants: grants}, options...)
}

// grantsBySignature serves fixed grants by signature and reports
// storage.ErrNotFound for any other.
type grantsBySignature struct {
	storage.ExchangeGrantStorage
	grants map[string]storage.ExchangeGrant
}

func (s *grantsBySignature) GetExchangeGrant(_ context.Context, signature string) (*storage.ExchangeGrant, error) {
	g, ok := s.grants[signature]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return &g, nil
}

// ActorTokenOptions shapes the actor token ActorTokenIDJAGRequestForTest
// presents. Without options the token is a valid unrestricted token signed with the
// handler's own key.
type ActorTokenOptions struct {
	Marked     bool     // adds the delegated-access marker
	Details    bool     // adds an authorization_details claim
	Expired    bool     // gives the token an expiry in the past
	Audience   []string // replaces the audience when non-empty
	ForeignKey bool     // signs with a key the handler does not trust
	Issuer     string   // replaces the issuer when non-empty (default testIssuer)
}

// ActorTokenIDJAGRequestForTest returns an ID-JAG handler whose client policy
// uses v as its actor validator, and a request that presents an actor token
// shaped by opts.
func ActorTokenIDJAGRequestForTest(
	t *testing.T, v ActorTokenValidator, opts ActorTokenOptions,
) (*RefreshTokenHandler, *fosite.AccessRequest) {
	t.Helper()
	tj := newTestJWKS(t)
	store := idJAGStore(idJAGRecord("ok", testRefreshClient, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG))
	h := idJAGHandler(t, store)
	self, err := NewSelfIssuedTokenValidator(tj.publicJWKS(), testIssuer, []string{testIssuer})
	require.NoError(t, err)
	h.selfValidator = self
	policy := h.clientPolicies[testRefreshClient]
	policy.ActorProfile = &ActorProfile{
		Name: "test", ActorTokenType: oauthproto.TokenTypeJWT,
		ActorValidatorRegistration: ActorValidatorRegistration{Validator: v, ActIssuer: "https://agents.example.com"},
	}
	h.clientPolicies[testRefreshClient] = policy

	claims := validClaims()
	if opts.Expired {
		claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	}
	if len(opts.Audience) > 0 {
		claims.Audience = opts.Audience
	}
	if opts.Issuer != "" {
		claims.Issuer = opts.Issuer
	}
	extra := terminalClaims(validExtraClaims(), opts.Marked, opts.Details)
	signer := tj
	if opts.ForeignKey {
		signer = newTestJWKS(t)
	}
	req := idJAGRequest("x.ok")
	req.Form.Set("actor_token", signer.signToken(t, claims, extra))
	req.Form.Set("actor_token_type", oauthproto.TokenTypeJWT)
	return h, req
}
