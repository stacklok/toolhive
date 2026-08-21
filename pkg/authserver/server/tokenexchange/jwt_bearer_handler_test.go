// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const testTokenEndpoint = "https://auth.example.com/oauth/token"

type testJWTBearerAssertionValidator struct {
	calls  int
	err    error
	claims *ValidatedClaims
}

func (v *testJWTBearerAssertionValidator) ValidateJWTBearerAssertion(
	context.Context, string, string,
) (*ValidatedClaims, error) {
	v.calls++
	if v.err != nil {
		return nil, v.err
	}
	if v.claims != nil {
		return v.claims, nil
	}
	return &ValidatedClaims{}, nil
}

func newJWTBearerRequest(form map[string][]string) *fosite.AccessRequest {
	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
	req.Form = form
	return req
}

// contextWithHTTPRequest mirrors what fosite.NewAccessRequest stashes into
// context (see access_request_handler.go) so CanSkipClientAuth can inspect
// HTTP Basic auth the way it does at runtime.
func contextWithHTTPRequest(t *testing.T, basicUser, basicPass string) context.Context {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, testTokenEndpoint, nil)
	if basicUser != "" || basicPass != "" {
		httpReq.SetBasicAuth(basicUser, basicPass)
	}
	return context.WithValue(context.Background(), fosite.RequestContextKey, httpReq)
}

func signAssertionWithType(t *testing.T, tj *testJWKS, typ any) string {
	t.Helper()

	options := &jose.SignerOptions{}
	if typ != nil {
		options.WithHeader(jose.HeaderType, typ)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: tj.jwk}, options)
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(validClaims()).Serialize()
	require.NoError(t, err)
	return raw
}

func TestJWTBearerHandler_MatchingAndClientAuth(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	validator := &testJWTBearerAssertionValidator{}
	h, err := newJWTBearerHandler(validator, testTokenEndpoint)
	require.NoError(t, err)

	tests := []struct {
		name         string
		grantTypes   fosite.Arguments
		assertion    string
		wantHandles  bool
		wantSkipAuth bool
	}{
		{
			name:         "plain assertion matches and skips client authentication",
			grantTypes:   fosite.Arguments{oauthproto.GrantTypeJWTBearer},
			assertion:    signAssertionWithType(t, tj, nil),
			wantHandles:  true,
			wantSkipAuth: true,
		},
		{
			name:         "ID-JAG assertion is left for its bound handler",
			grantTypes:   fosite.Arguments{oauthproto.GrantTypeJWTBearer},
			assertion:    signAssertionWithType(t, tj, idJAGJWTType),
			wantHandles:  false,
			wantSkipAuth: false,
		},
		{
			name:         "other grant does not match",
			grantTypes:   fosite.Arguments{"client_credentials"},
			assertion:    signAssertionWithType(t, tj, nil),
			wantHandles:  false,
			wantSkipAuth: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := newJWTBearerRequest(map[string][]string{"assertion": {tt.assertion}})
			req.GrantTypes = tt.grantTypes
			ctx := contextWithHTTPRequest(t, "", "")
			assert.Equal(t, tt.wantHandles, h.CanHandleTokenEndpointRequest(ctx, req))
			assert.Equal(t, tt.wantSkipAuth, h.CanSkipClientAuth(ctx, req))
		})
	}
}

func TestValidateJWTBearerPolicy_RejectsMalformedResourceURI(t *testing.T) {
	t.Parallel()

	now := time.Now()
	policy := &JWTBearerGrantPolicy{
		maxAssertionAge: time.Hour,
		SubjectBindings: []JWTBearerSubjectBinding{{
			Subject:          "external-subject",
			AllowedResources: []string{"not-a-uri"},
		}},
	}
	claims := &ValidatedClaims{
		Subject:  "external-subject",
		IssuedAt: now,
		Expiry:   now.Add(time.Minute),
	}

	_, err := validateJWTBearerPolicy(url.Values{"resource": {"not-a-uri"}}, claims, policy)
	require.Error(t, err)
	assert.ErrorIs(t, err, server.ErrInvalidTarget)
}

func TestJWTBearerHandler_AssertionFormAndType(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	tests := []struct {
		name           string
		assertions     []string
		wantCalls      int
		wantErrContain string
		wantErrIs      error
	}{
		{
			// newJWTBearerHandler alone (no policies/consumer wired) always
			// fails closed after validating the assertion — this handler is
			// never wired into a real token endpoint on its own; see its doc
			// comment. wantCalls == 1 proves form/type validation passed and
			// the validator ran.
			name:           "single plain assertion is validated but the unwired handler fails closed",
			assertions:     []string{signAssertionWithType(t, tj, nil)},
			wantCalls:      1,
			wantErrContain: "issuer is not enabled",
			wantErrIs:      fosite.ErrInvalidGrant,
		},
		{
			name:           "missing assertion is rejected",
			wantErrContain: "required exactly once",
			wantErrIs:      fosite.ErrInvalidRequest,
		},
		{
			name:           "empty assertion is rejected",
			assertions:     []string{""},
			wantErrContain: "required exactly once",
			wantErrIs:      fosite.ErrInvalidRequest,
		},
		{
			name:           "repeated assertion is rejected",
			assertions:     []string{"first", "second"},
			wantErrContain: "required exactly once",
			wantErrIs:      fosite.ErrInvalidRequest,
		},
		{
			// RFC 7523: JWT-validity failures (malformed, unsupported
			// signature/typ) are invalid_grant, not invalid_request — that
			// code is reserved for malformed request parameters.
			name:           "malformed assertion is rejected",
			assertions:     []string{"not-a-jwt"},
			wantErrContain: "not a valid signed JWT",
			wantErrIs:      fosite.ErrInvalidGrant,
		},
		{
			name:           "non string typ is rejected",
			assertions:     []string{signAssertionWithType(t, tj, 1)},
			wantErrContain: "typ header must be a string",
			wantErrIs:      fosite.ErrInvalidGrant,
		},
		{
			name:           "unknown typ is rejected",
			assertions:     []string{signAssertionWithType(t, tj, "application/example+jwt")},
			wantErrContain: "typ header is not supported",
			wantErrIs:      fosite.ErrInvalidGrant,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			validator := &testJWTBearerAssertionValidator{}
			h, err := newJWTBearerHandler(validator, testTokenEndpoint)
			require.NoError(t, err)
			req := newJWTBearerRequest(map[string][]string{"assertion": tt.assertions})

			err = h.HandleTokenEndpointRequest(context.Background(), req)
			if tt.wantErrContain != "" {
				require.Error(t, err)
				var rfcErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &rfcErr), "expected fosite RFC6749Error")
				assert.Contains(t, rfcErr.Reason(), tt.wantErrContain)
				assert.ErrorIs(t, err, tt.wantErrIs)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantCalls, validator.calls)
		})
	}
}

func TestNewJWTBearerHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validator JWTBearerAssertionValidator
		endpoint  string
		wantErr   string
	}{
		{name: "nil validator", endpoint: testTokenEndpoint, wantErr: "must not be nil"},
		{name: "empty endpoint", validator: &testJWTBearerAssertionValidator{}, wantErr: "must not be empty"},
		{name: "invalid endpoint", validator: &testJWTBearerAssertionValidator{}, endpoint: "://", wantErr: "is invalid"},
		{name: "valid", validator: &testJWTBearerAssertionValidator{}, endpoint: testTokenEndpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, err := newJWTBearerHandler(tt.validator, tt.endpoint)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, h)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, h)
		})
	}
}

func TestJWTBearerHandler_CanSkipClientAuthRejectsFormCredentials(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	h, err := newJWTBearerHandler(&testJWTBearerAssertionValidator{}, testTokenEndpoint)
	require.NoError(t, err)

	tests := []struct {
		name      string
		form      url.Values
		basicUser string
		basicPass string
		noReqCtx  bool
		want      bool
	}{
		{name: "credential-free assertion", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}}, want: true},
		{name: "client ID", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}, "client_id": {"client"}}},
		{name: "client secret", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}, "client_secret": {"secret"}}},
		{name: "client assertion", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}, "client_assertion": {"credential"}}},
		{name: "client assertion type", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}, "client_assertion_type": {"urn:ietf:params:oauth:client-assertion-type:jwt-bearer"}}},
		// CanSkipClientAuth reads url.Values.Get, so blank form values do not
		// constitute credentials at this Fosite handler seam.
		{name: "blank form credential values", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}, "client_id": {""}, "client_secret": {""}, "client_assertion": {""}, "client_assertion_type": {""}}, want: true},
		// Regression coverage for the Basic-auth bypass: fosite discards the
		// AuthenticateClient error only when CanSkipClientAuth is true, and it
		// falls back to HTTP Basic auth when no form credentials are present.
		{name: "HTTP Basic auth present with no form credentials", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}}, basicUser: "client", basicPass: "secret", want: false},
		// Regression coverage: an empty-username Basic credential (e.g.
		// "Basic base64(\":secret\")") is a syntactically valid RFC 7617 header
		// and must not be mistaken for "no credentials supplied".
		{name: "HTTP Basic auth with empty username", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}}, basicUser: "", basicPass: "secret", want: false},
		{name: "no HTTP request in context fails closed", form: url.Values{"assertion": {signAssertionWithType(t, tj, nil)}}, noReqCtx: true, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := newJWTBearerRequest(tt.form)
			ctx := context.Background()
			if !tt.noReqCtx {
				ctx = contextWithHTTPRequest(t, tt.basicUser, tt.basicPass)
			}
			assert.Equal(t, tt.want, h.CanSkipClientAuth(ctx, req))
		})
	}
}

func TestValidateJWTBearerPolicy_ResourceAndPolicyErrors(t *testing.T) {
	t.Parallel()

	now := time.Now()
	claims := &ValidatedClaims{Subject: "subject", IssuedAt: now, Expiry: now.Add(time.Minute)}
	policy := &JWTBearerGrantPolicy{maxAssertionAge: time.Hour, SubjectBindings: []JWTBearerSubjectBinding{{
		Subject: "subject", AllowedResources: []string{"https://mcp.example.com"},
	}}}
	tests := []struct {
		name     string
		form     url.Values
		claims   *ValidatedClaims
		want     error
		wantCode string
	}{
		{name: "missing resource", form: url.Values{}, claims: claims, want: fosite.ErrInvalidRequest},
		{name: "repeated resource", form: url.Values{"resource": {"https://mcp.example.com", "https://mcp.example.com"}}, claims: claims, want: fosite.ErrInvalidRequest},
		{
			name:     "unauthorized resource",
			form:     url.Values{"resource": {"https://other.example.com"}},
			claims:   claims,
			wantCode: "invalid_target",
		},
		{name: "unbound subject", form: url.Values{"resource": {"https://mcp.example.com"}}, claims: &ValidatedClaims{Subject: "other", IssuedAt: now, Expiry: now.Add(time.Minute)}, want: fosite.ErrInvalidGrant},
		{name: "assertion too old", form: url.Values{"resource": {"https://mcp.example.com"}}, claims: &ValidatedClaims{Subject: "subject", IssuedAt: now.Add(-2 * time.Hour), Expiry: now}, want: fosite.ErrInvalidGrant},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateJWTBearerPolicy(tt.form, tt.claims, policy)
			require.Error(t, err)
			if tt.wantCode != "" {
				var oauthErr *fosite.RFC6749Error
				require.True(t, errors.As(err, &oauthErr))
				assert.Equal(t, tt.wantCode, oauthErr.ErrorField)
				return
			}
			assert.ErrorIs(t, err, tt.want)
		})
	}
}

func TestValidateAssertionType_RejectsUntrustedAlgorithms(t *testing.T) {
	t.Parallel()

	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"subject"}`)) + "."
	hmacSigner, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.HS256, Key: []byte("01234567890123456789012345678901")}, nil)
	require.NoError(t, err)
	hmacToken, err := jwt.Signed(hmacSigner).Claims(validClaims()).Serialize()
	require.NoError(t, err)

	for name, raw := range map[string]string{"alg none": none, "HS256": hmacToken} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateAssertionType(raw)
			require.Error(t, err)
			assert.ErrorIs(t, err, fosite.ErrInvalidGrant)
		})
	}
}

type assertionConsumerStorage struct{ storage.Storage }

func (assertionConsumerStorage) ConsumeAssertionJWT(context.Context, string, string, string, time.Time) error {
	return nil
}

func TestAssertionJWTConsumer_UnwrapsCIMDAtConstruction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		base    storage.Storage
		wantErr string
	}{
		{name: "unsupported backend fails", base: storageWithoutAssertionConsumer{}, wantErr: "does not implement"},
		{name: "supported backend succeeds", base: assertionConsumerStorage{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decorated, err := storage.NewCIMDStorageDecorator(tt.base, storage.CIMDDecoratorConfig{Enabled: true, CacheMaxSize: 1, FallbackTTL: time.Minute})
			require.NoError(t, err)
			consumer, err := assertionJWTConsumer(decorated)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Nil(t, consumer)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, consumer)
		})
	}
}

type storageWithoutAssertionConsumer struct{ storage.Storage }

type testAssertionJWTConsumer struct{ err error }

func (c testAssertionJWTConsumer) ConsumeAssertionJWT(context.Context, string, string, string, time.Time) error {
	return c.err
}

func TestJWTBearerHandler_ConsumeAssertionJWTErrors(t *testing.T) {
	t.Parallel()

	const (
		issuer   = "https://idp.example.com"
		subject  = "ext-user"
		resource = "https://mcp.example.com"
	)
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

	tests := []struct {
		name        string
		consumerErr error
		wantErrIs   error
	}{
		{name: "replay is rejected as invalid_grant", consumerErr: fosite.ErrJTIKnown, wantErrIs: fosite.ErrInvalidGrant},
		{name: "storage failure surfaces as a server error", consumerErr: errors.New("redis: connection refused"), wantErrIs: fosite.ErrServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tj := newTestJWKS(t)
			now := time.Now()
			validator := &testJWTBearerAssertionValidator{claims: &ValidatedClaims{
				Issuer:   issuer,
				Subject:  subject,
				JWTID:    "jti-consume-test",
				IssuedAt: now,
				Expiry:   now.Add(30 * time.Minute),
			}}
			handler, err := newJWTBearerIssuanceHandler(
				validator,
				testTokenEndpoint,
				testAssertionJWTConsumer{err: tt.consumerErr},
				&fosite.Config{AccessTokenLifespan: time.Hour},
				&mockAccessTokenStrategy{},
				&mockAccessTokenStorage{},
				resolvedIssuers,
			)
			require.NoError(t, err)

			req := fosite.NewAccessRequest(&session.Session{})
			req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
			req.Form = map[string][]string{
				"assertion": {signAssertionWithType(t, tj, nil)},
				"resource":  {resource},
			}

			err = handler.HandleTokenEndpointRequest(context.Background(), req)
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.wantErrIs)
		})
	}
}

func TestNewJWTBearerIssuanceHandler_RejectsMissingDependencies(t *testing.T) {
	t.Parallel()

	validator := &testJWTBearerAssertionValidator{}
	tests := []struct {
		name     string
		consumer storage.AssertionJWTConsumer
		wantErr  string
	}{
		{name: "missing replay consumer", wantErr: "storage must implement"},
		{name: "missing issuance dependencies", consumer: testAssertionJWTConsumer{}, wantErr: "issuance dependencies must not be nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, err := newJWTBearerIssuanceHandler(validator, testTokenEndpoint, tt.consumer, nil, nil, nil, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Nil(t, h)
		})
	}
}
