// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	storagemocks "github.com/stacklok/toolhive/pkg/authserver/storage/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	"github.com/stacklok/toolhive/pkg/authserver/subject/mocks"
	"github.com/stacklok/toolhive/pkg/authserver/subjectresolver"
)

const (
	resolveTestUpstreamIssuer = "https://signin.example.com"
	resolveTestClaim          = "oid"
	resolveTestValue          = "upstream-value-x7"
	resolveTestUser           = "local-user-1"
)

const (
	wantHintUpstreamSubjectBad  = "The ID-JAG assertion has a missing or malformed upstream_subject claim."
	wantHintUpstreamSubjectFrgn = "The ID-JAG assertion upstream_subject does not name this server's " +
		"sign-in identity provider and subject claim."
)

// serverErrorDescription is what the client sees when the subject cannot be
// resolved.
var serverErrorDescription = fosite.ErrServerError.WithHint("The subject could not be resolved.").GetDescription()

func upstreamSubjectClaim(iss, claim, value any) map[string]any {
	return map[string]any{"iss": iss, "claim": claim, "value": value}
}

func resolveClaims(upstream any) *ValidatedClaims {
	claims := validIDJAGClaims()
	claims.Extra["resource"] = idJAGTestResource
	if upstream != nil {
		claims.Extra[claimnames.UpstreamSubjectKey] = upstream
	}
	return claims
}

func validUpstreamSubject() map[string]any {
	return upstreamSubjectClaim(resolveTestUpstreamIssuer, resolveTestClaim, resolveTestValue)
}

type resolveFixture struct {
	h        *IDJAGHandler
	policy   *JWTBearerGrantPolicy
	consumer *recordingAssertionConsumer
	log      *strings.Builder
	// client redeems; the zero value is a plain client, as in the handler tests.
	client fosite.Client
	// req is the request the last redeem completed.
	req *fosite.AccessRequest
}

// newResolveFixture builds a resolve handler whose resolver is set directly,
// as IDJAGIssuanceFactory would.
func newResolveFixture(t *testing.T, resolver subject.Resolver) *resolveFixture {
	t.Helper()
	consumer := &recordingAssertionConsumer{}
	require.NotEqual(t, idJAGTestIssuer, resolveTestUpstreamIssuer,
		"the log assertions rely on the two issuers differing")
	deps := subjectModeDeps(t, resolvePolicyWithActors(), consumer)
	var buf strings.Builder
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h, err := newIDJAGIssuanceHandler(deps, nil, logger)
	require.NoError(t, err)
	h.resolver = resolver
	buf.Reset()
	return &resolveFixture{
		h: h, policy: h.policies[idJAGTestIssuer], consumer: consumer, log: &buf,
		client: &fosite.DefaultClient{ID: idJAGTestClientID},
	}
}

// redeem runs the step as the handler does: complete runs only when the
// subject resolved.
func (f *resolveFixture) redeem(t *testing.T, claims *ValidatedClaims) (redemption, error) {
	t.Helper()
	d, err := f.h.idJAGRedemption(context.Background(), claims, f.policy, f.client, idJAGTestResource)
	if err != nil {
		return d, err
	}
	f.req = newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	return d, f.h.redeemer.complete(context.Background(), f.req, d)
}

// requireSubjectLog asserts the log holds exactly one refusal line with the
// issuer, the client and wantReason, and none of the values a log must not
// carry.
func requireSubjectLog(t *testing.T, buf *strings.Builder, wantReason string) {
	t.Helper()
	var lines []string
	for _, line := range logLinesAt(buf, "DEBUG") {
		if strings.Contains(line, "refused at subject resolution") {
			lines = append(lines, line)
		}
	}
	require.Len(t, lines, 1)
	assert.Contains(t, lines[0], "issuer="+idJAGTestIssuer)
	assert.Contains(t, lines[0], "client_id="+idJAGTestClientID)
	assert.Contains(t, lines[0], "reason="+wantReason)
	assert.NotContains(t, lines[0], "error=")
	for _, secret := range []string{idJAGTestSubject, "id-jag-jti", resolveTestValue, resolveTestUpstreamIssuer} {
		assert.NotContains(t, buf.String(), secret)
	}
}

func TestIDJAGResolve_RefusedBeforeResolver(t *testing.T) {
	t.Parallel()

	const badDesc = "invalid_assertion: " + wantHintUpstreamSubjectBad
	tests := []struct {
		name         string
		upstream     any
		client       fosite.Client // nil keeps the fixture's plain client
		dropResource bool
		wantCode     string
		wantDesc     string
		wantReason   string
	}{
		{name: "claim missing", wantCode: "invalid_grant", wantDesc: badDesc, wantReason: reasonUpstreamSubjectBad},
		{name: "claim is a string", upstream: "idp#value", wantCode: "invalid_grant", wantDesc: badDesc,
			wantReason: reasonUpstreamSubjectBad},
		{name: "iss missing", upstream: map[string]any{"claim": "oid", "value": "v"}, wantCode: "invalid_grant",
			wantDesc: badDesc, wantReason: reasonUpstreamSubjectBad},
		{name: "claim is a number", upstream: upstreamSubjectClaim(resolveTestUpstreamIssuer, 7, "v"),
			wantCode: "invalid_grant", wantDesc: badDesc, wantReason: reasonUpstreamSubjectBad},
		{name: "value empty", upstream: upstreamSubjectClaim(resolveTestUpstreamIssuer, "oid", ""),
			wantCode: "invalid_grant", wantDesc: badDesc, wantReason: reasonUpstreamSubjectBad},
		{name: "client not permitted", upstream: validUpstreamSubject(),
			client:   registration.MarkDCRIssued(&fosite.DefaultClient{ID: idJAGTestClientID}),
			wantCode: "access_denied", wantDesc: "client_not_permitted: " + hintClientNotPermitted,
			wantReason: reasonClientNotPermitted},
		// A resolve redemption takes its resource from the signed claim only.
		{name: "resource claim missing", upstream: validUpstreamSubject(), dropResource: true,
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: " + hintResourceClaimMissing,
			wantReason: reasonResourceClaimMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := mocks.NewMockResolver(gomock.NewController(t))
			resolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Times(0)
			f := newResolveFixture(t, resolver)
			if tt.client != nil {
				f.client = tt.client
			}
			claims := resolveClaims(tt.upstream)
			if tt.dropResource {
				delete(claims.Extra, "resource")
			}

			_, err := f.redeem(t, claims)

			requireRefusal(t, err, tt.wantCode, tt.wantDesc)
			assert.Empty(t, f.consumer.key, "the ID-JAG must stay unused")
			requireSubjectLog(t, f.log, tt.wantReason)
		})
	}
}

func TestIDJAGResolve_Resolver(t *testing.T) {
	t.Parallel()

	wantRef := subject.Ref{Issuer: resolveTestUpstreamIssuer, Claim: resolveTestClaim, Value: resolveTestValue}
	tests := []struct {
		name          string
		user          string
		err           error
		noResolver    bool
		wantStatus    int
		wantCode      string
		wantDesc      string
		wantLogReason string
	}{
		{name: "issuer or claim not configured", err: subject.ErrNotConfigured, wantStatus: http.StatusBadRequest,
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: " + wantHintUpstreamSubjectFrgn,
			wantLogReason: reasonUpstreamSubjectFrgn},
		{name: "not configured, wrapped", err: fmt.Errorf("lookup: %w", subject.ErrNotConfigured),
			wantStatus: http.StatusBadRequest, wantCode: "invalid_grant",
			wantDesc: "invalid_assertion: " + wantHintUpstreamSubjectFrgn, wantLogReason: reasonUpstreamSubjectFrgn},
		{name: "unknown", err: subject.ErrUnknown, wantStatus: http.StatusBadRequest,
			wantCode: "access_denied", wantDesc: "subject_unknown", wantLogReason: reasonSubjectUnknown},
		{name: "ambiguous", err: subject.ErrAmbiguous, wantStatus: http.StatusBadRequest,
			wantCode: "access_denied", wantDesc: "subject_unknown", wantLogReason: reasonSubjectUnknown},
		{name: "empty user with nil error", wantStatus: http.StatusBadRequest,
			wantCode: "access_denied", wantDesc: "subject_unknown", wantLogReason: reasonSubjectUnknown},
		{name: "unavailable", err: fmt.Errorf("lookup: %w", subject.ErrUnavailable), wantStatus: http.StatusInternalServerError,
			wantCode: "server_error", wantDesc: serverErrorDescription, wantLogReason: reasonResolverUnavailable},
		{name: "other error", err: errors.New("boom"), wantStatus: http.StatusInternalServerError,
			wantCode: "server_error", wantDesc: serverErrorDescription, wantLogReason: reasonResolverUnavailable},
		{name: "no resolver configured", noResolver: true, wantStatus: http.StatusInternalServerError,
			wantCode: "server_error", wantDesc: serverErrorDescription, wantLogReason: reasonResolverUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var resolver subject.Resolver
			if !tt.noResolver {
				mock := mocks.NewMockResolver(gomock.NewController(t))
				mock.EXPECT().Resolve(gomock.Any(), wantRef).Return(tt.user, tt.err).Times(1)
				resolver = mock
			}
			f := newResolveFixture(t, resolver)

			_, err := f.redeem(t, resolveClaims(validUpstreamSubject()))

			requireRefusalStatus(t, err, tt.wantStatus, tt.wantCode, tt.wantDesc)
			assert.Empty(t, f.consumer.key, "the ID-JAG must stay unused")
			requireSubjectLog(t, f.log, tt.wantLogReason)
		})
	}
}

// TestIDJAGResolve_FoundIssuesMarkedSessionlessToken checks the token a
// resolved redemption issues: it carries the delegated-access marker and
// no_upstream_session but no tsid (a usable tsid prevents delegated admission
// at vMCP). Redemption grants no scope, and populating the response adds no
// refresh token. The HTTP-level test covers a client that asks for
// offline_access. Other fosite handlers are not exercised here.
func TestIDJAGResolve_FoundIssuesMarkedSessionlessToken(t *testing.T) {
	t.Parallel()

	mock := mocks.NewMockResolver(gomock.NewController(t))
	mock.EXPECT().Resolve(gomock.Any(), subject.Ref{
		Issuer: resolveTestUpstreamIssuer, Claim: resolveTestClaim, Value: resolveTestValue,
	}).Return(resolveTestUser, nil).Times(1)
	f := newResolveFixture(t, mock)

	d, err := f.redeem(t, resolveClaims(validUpstreamSubject()))

	require.NoError(t, err)
	assert.Equal(t, resolveTestUser, d.subject)
	assert.True(t, d.delegatedAccess)
	assert.Equal(t, "id-jag-jti", f.consumer.key)
	sess, ok := f.req.GetSession().(*session.Session)
	require.True(t, ok)
	assert.Equal(t, resolveTestUser, sess.GetSubject())
	assert.Equal(t, true, sess.JWTClaims.Extra[claimnames.DelegatedAccessKey])
	assert.Equal(t, fosite.Arguments{idJAGTestResource}, f.req.GetGrantedAudience())
	assert.NotContains(t, sess.JWTClaims.Extra, claimnames.TokenSessionIDKey)
	assert.Equal(t, true, sess.JWTClaims.Extra[claimnames.NoUpstreamSessionKey])

	assert.Empty(t, f.req.GetGrantedScopes())
	resp := fosite.NewAccessResponse()
	require.NoError(t, f.h.PopulateTokenEndpointResponse(t.Context(), f.req, resp))
	assert.NotEmpty(t, resp.GetAccessToken())
	assert.NotContains(t, resp.ToMap(), "refresh_token")
}

// TestIDJAGResolve_FillsClientLifespan is a wiring test: the resolve path
// copies the client's lifespan into the redemption. The clientLifespan check
// is the assertion that depends on it; the expiry check only confirms the
// session is still capped, and would pass without the lifespan. The lifetime
// order is covered by TestRedeemer_DelegatedAccessLifetime.
func TestIDJAGResolve_FillsClientLifespan(t *testing.T) {
	t.Parallel()

	mock := mocks.NewMockResolver(gomock.NewController(t))
	mock.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(resolveTestUser, nil).Times(1)
	f := newResolveFixture(t, mock)
	f.h.clientLifespans = map[string]time.Duration{idJAGTestClientID: 20 * time.Minute}
	claims := resolveClaims(validUpstreamSubject())
	claims.Expiry = time.Now().Add(time.Hour)

	d, err := f.redeem(t, claims)

	require.NoError(t, err)
	assert.True(t, d.delegatedAccess)
	assert.Equal(t, 20*time.Minute, d.clientLifespan)
	assert.WithinDuration(t, time.Now().Add(delegatedAccessMarkerCap), f.req.GetSession().GetExpiresAt(fosite.AccessToken),
		5*time.Second)
}

func TestIDJAGIssuanceFactory_SubjectResolution(t *testing.T) {
	t.Parallel()

	resolveIssuers := []TrustedIssuer{{
		IssuerURL:              idJAGTestIssuer,
		ExpectedAudience:       idJAGTestResource,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant:         subjectModePolicy(SubjectModeResolve, nil),
	}}
	namespacedIssuers := []TrustedIssuer{{
		IssuerURL:              idJAGTestIssuer,
		ExpectedAudience:       idJAGTestResource,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: subjectModePolicy(SubjectModeNamespaced, []JWTBearerSubjectBinding{
			{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
		}),
	}}
	const wantNoResolver = "a subject resolver is required when a trusted issuer has subject_mode resolve"
	resolver := mocks.NewMockResolver(gomock.NewController(t))
	build := func(t *testing.T, issuers []TrustedIssuer, resolver subject.Resolver) (server.Factory, error) {
		t.Helper()
		cfg := buildTestAuthServerConfig(t)
		shared, err := NewSharedTrustedIssuerValidator(cfg, issuers)
		require.NoError(t, err)
		t.Cleanup(func() { _ = shared.Close() })
		return IDJAGIssuanceFactory(issuers, shared, nil, resolver)
	}

	t.Run("resolve issuer and no resolver", func(t *testing.T) {
		t.Parallel()
		_, err := build(t, resolveIssuers, nil)
		assert.EqualError(t, err, wantNoResolver)
	})
	t.Run("namespaced issuers need no resolution", func(t *testing.T) {
		t.Parallel()
		_, err := build(t, namespacedIssuers, nil)
		require.NoError(t, err)
	})
	t.Run("resolution reaches the handler", func(t *testing.T) {
		t.Parallel()
		f, err := build(t, resolveIssuers, resolver)
		require.NoError(t, err)
		result, err := f(buildTestAuthServerConfig(t), &jwtBearerFactoryStorage{
			fakeFactoryStorage:         fakeFactoryStorage{mockAccessTokenStorage: &mockAccessTokenStorage{}},
			recordingAssertionConsumer: &recordingAssertionConsumer{},
		}, &mockAccessTokenStrategy{})
		require.NoError(t, err)
		h, ok := result.(*IDJAGHandler)
		require.True(t, ok, "factory closure must return *IDJAGHandler, got %T", result)
		assert.Equal(t, subject.Resolver(resolver), h.resolver)
	})
}

func TestIDJAGResolve_NamespacedSession(t *testing.T) {
	t.Parallel()

	consumer := &recordingAssertionConsumer{}
	deps := subjectModeDeps(t, subjectModePolicy(SubjectModeNamespaced, []JWTBearerSubjectBinding{
		{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
	}), consumer)
	h := newQuietIDJAGHandler(t, deps)

	d, err := h.idJAGRedemption(context.Background(), validIDJAGClaims(), h.policies[idJAGTestIssuer],
		&fosite.DefaultClient{ID: idJAGTestClientID}, idJAGTestResource)
	require.NoError(t, err)
	req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	require.NoError(t, h.redeemer.complete(context.Background(), req, d))

	sess, ok := req.GetSession().(*session.Session)
	require.True(t, ok)
	assert.Equal(t, idJAGTestIssuer+"#"+idJAGTestSubject, sess.GetSubject())
	assert.NotContains(t, sess.JWTClaims.Extra, claimnames.DelegatedAccessKey)
}

func TestIDJAGResolve_HandlerDoesNotNarrowResolver(t *testing.T) {
	t.Parallel()

	const (
		secondIssuer = "https://second.example.com"
		secondClaim  = "uid"
		secondValue  = "second-value"
		secondUser   = "local-user-2"
	)
	tests := []struct {
		name       string
		upstream   map[string]any
		wantUser   string
		wantCode   string
		wantDesc   string
		wantReason string
	}{
		{name: "first issuer", upstream: validUpstreamSubject(), wantUser: resolveTestUser},
		{name: "second issuer", upstream: upstreamSubjectClaim(secondIssuer, secondClaim, secondValue),
			wantUser: secondUser},
		{name: "foreign issuer", upstream: upstreamSubjectClaim("https://other.example.com", resolveTestClaim, "v"),
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: " + wantHintUpstreamSubjectFrgn,
			wantReason: reasonUpstreamSubjectFrgn},
		{name: "claim of another issuer", upstream: upstreamSubjectClaim(secondIssuer, resolveTestClaim, secondValue),
			wantCode: "invalid_grant", wantDesc: "invalid_assertion: " + wantHintUpstreamSubjectFrgn,
			wantReason: reasonUpstreamSubjectFrgn},
		{name: "unknown value", upstream: upstreamSubjectClaim(resolveTestUpstreamIssuer, resolveTestClaim, "nobody"),
			wantCode: "access_denied", wantDesc: descSubjectUnknown, wantReason: reasonSubjectUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := storagemocks.NewMockUserStorage(gomock.NewController(t))
			for provider, ids := range map[string]map[string]string{
				"first":  {resolveTestValue: resolveTestUser},
				"second": {secondValue: secondUser},
			} {
				for value, user := range ids {
					store.EXPECT().GetProviderIdentity(gomock.Any(), provider, value).
						Return(&storage.ProviderIdentity{UserID: user}, nil).AnyTimes()
					store.EXPECT().GetUser(gomock.Any(), user).Return(&storage.User{ID: user}, nil).AnyTimes()
				}
				store.EXPECT().GetProviderIdentity(gomock.Any(), provider, "nobody").
					Return(nil, storage.ErrNotFound).AnyTimes()
			}
			resolver, err := subjectresolver.New(map[string]subjectresolver.Provider{
				resolveTestUpstreamIssuer: {
					Name: "first", SubjectClaim: resolveTestClaim,
					RowIssuer: resolveTestUpstreamIssuer, RowSubjectClaim: resolveTestClaim,
				},
				secondIssuer: {
					Name: "second", SubjectClaim: secondClaim, RowIssuer: secondIssuer, RowSubjectClaim: secondClaim,
				},
			}, store)
			require.NoError(t, err)
			f := newResolveFixture(t, resolver)

			d, err := f.redeem(t, resolveClaims(tt.upstream))

			if tt.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, tt.wantUser, d.subject)
				return
			}
			requireRefusal(t, err, tt.wantCode, tt.wantDesc)
			assert.Empty(t, f.consumer.key, "the ID-JAG must stay unused")
			requireSubjectLog(t, f.log, tt.wantReason)
		})
	}
}
