// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/google/uuid"
	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	mintTestIssuer        = "https://as1.example.com"
	mintTestTargetIssuer  = "https://as2.example.com"
	mintTestResource      = "https://gw.example.com/mcp"
	mintTestTargetClient  = "broker-at-as2"
	mintTestRequesting    = "broker"
	mintTestSubject       = "user-1"
	mintTestUpstreamIss   = "https://idp.example.com"
	mintTestUpstreamSub   = "idp-sub-1"
	mintTestStableClaim   = "oid"
	mintTestStableValue   = "oid-1"
	mintTestOtherClient   = "https://other-client-target.example.com"
	mintTestAudienceParam = "audience"
)

// mintTestNow has a fractional second, so a test sees whether exp is floored.
var mintTestNow = time.Unix(1_800_000_000, 500_000_000)

// recordingSigner counts its SignIDJAG calls and then signs with inner, or
// fails with err when it is set.
type recordingSigner struct {
	inner idJAGSigner
	err   error
	calls int
}

func (s *recordingSigner) SignIDJAG(ctx context.Context, claims map[string]any) (string, error) {
	s.calls++
	if s.err != nil {
		return "", s.err
	}
	return s.inner.SignIDJAG(ctx, claims)
}

// newMintTestSigner wraps the server's real Signer, so the tests run the typ
// guard and the kid the server puts in the header.
func newMintTestSigner(tj *testJWKS) *recordingSigner {
	return &recordingSigner{inner: server.NewSigner(&tj.jwk)}
}

func mintTestTarget() IDJAGTarget {
	return IDJAGTarget{
		Issuer:                mintTestTargetIssuer,
		ClientID:              mintTestTargetClient,
		Resources:             []string{mintTestResource, "https://gw.example.com/other"},
		Lifetime:              time.Minute,
		RequiredSubjectClaims: []string{"upstream_subject", "sub_id"},
	}
}

// sessionSubjectClaims returns the claims a login session records for a
// complete upstream identity, as the session package writes them, keyed by
// claim name. It also holds claims the core must never copy (client_id).
func sessionSubjectClaims(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	s := session.New(mintTestSubject, "", mintTestRequesting, session.UserClaims{})
	s.SetSubjectIdentifier(session.UpstreamSubject{
		Issuer: mintTestUpstreamIss, Subject: mintTestUpstreamSub,
		StableClaim: mintTestStableClaim, StableValue: mintTestStableValue,
	})
	out := make(map[string]json.RawMessage, len(s.JWTClaims.Extra))
	for name, value := range s.JWTClaims.Extra {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		out[name] = raw
	}
	require.Contains(t, out, claimnames.UpstreamSubjectKey)
	require.Contains(t, out, claimnames.SubjectIdentifierKey)
	return out
}

func mintTestMint(t *testing.T) idJAGMint {
	t.Helper()
	return idJAGMint{
		Issuer:             mintTestIssuer,
		RequestingClientID: mintTestRequesting,
		Subject:            idJAGSubject{Subject: mintTestSubject, Claims: sessionSubjectClaims(t)},
		Target:             mintTestTarget(),
		Resource:           mintTestResource,
	}
}

// decodeJWTPart decodes one base64url part of a compact JWT, keeping numbers
// as json.Number so a test sees whether a value was an integer.
func decodeJWTPart(t *testing.T, token string, part int) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	raw, err := base64.RawURLEncoding.DecodeString(parts[part])
	require.NoError(t, err)
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out map[string]any
	require.NoError(t, dec.Decode(&out))
	return out
}

func requireMintOAuthError(t *testing.T, err error, code string) *fosite.RFC6749Error {
	t.Helper()
	var rfc *fosite.RFC6749Error
	require.ErrorAs(t, err, &rfc)
	assert.Equal(t, code, rfc.ErrorField)
	return rfc
}

func requireMintFailure(t *testing.T, err error) {
	t.Helper()
	rfc := requireMintOAuthError(t, err, fosite.ErrServerError.ErrorField)
	assert.Equal(t, fosite.ErrServerError.WithHint("The ID-JAG could not be minted.").GetDescription(), rfc.GetDescription(),
		"a mint failure carries no detail for the client")
}

func TestMintIDJAGGolden(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	signer := newMintTestSigner(tj)
	responder := fosite.NewAccessResponse()

	minted, reason, err := respondWithIDJAG(context.Background(), signer, allowAllIDJAGPolicy, mintTestMint(t), responder, mintTestNow)
	require.NoError(t, err)
	require.Empty(t, reason)

	header := decodeJWTPart(t, minted.Token, 0)
	payload := decodeJWTPart(t, minted.Token, 1)
	iat := json.Number("1800000000")
	exp := json.Number("1800000060")

	t.Run("the token is signed once with the ID-JAG typ", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, 1, signer.calls)
		assert.Equal(t, oauthproto.JWTTypeIDJAG, header["typ"])
	})

	t.Run("header", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, map[string]any{"typ": oauthproto.JWTTypeIDJAG, "alg": "ES256", "kid": tj.jwk.KeyID}, header)
	})

	t.Run("the jti is a UUID", func(t *testing.T) {
		t.Parallel()
		assert.Len(t, minted.JTI, 36)
		_, err := uuid.Parse(minted.JTI)
		require.NoError(t, err)
	})

	t.Run("claim names and JSON types", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, map[string]any{
			"iss":       mintTestIssuer,
			"sub":       mintTestSubject,
			"aud":       mintTestTargetIssuer,
			"client_id": mintTestTargetClient,
			"jti":       minted.JTI,
			"iat":       iat,
			"exp":       exp,
			"resource":  mintTestResource,
			claimnames.UpstreamSubjectKey: map[string]any{
				"iss": mintTestUpstreamIss, "claim": mintTestStableClaim, "value": mintTestStableValue,
			},
			claimnames.SubjectIdentifierKey: map[string]any{"format": "iss_sub", "iss": mintTestUpstreamIss, "sub": mintTestUpstreamSub},
		}, payload, "aud and resource are strings, iat and exp integers; no nbf, no scope, no act")
	})

	t.Run("the signature verifies with the published key", func(t *testing.T) {
		t.Parallel()
		parsed, err := jose.ParseSignedCompact(minted.Token, []jose.SignatureAlgorithm{jose.ES256})
		require.NoError(t, err)
		_, err = parsed.Verify(tj.publicJWKS().Key(tj.jwk.KeyID)[0].Key)
		require.NoError(t, err)
	})

	t.Run("response members, and no scope member for a request without scope", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, map[string]any{
			"access_token":      minted.Token,
			"issued_token_type": oauthproto.TokenTypeIDJAG,
			"token_type":        "N_A",
			"expires_in":        int64(60),
		}, responder.ToMap())
		iatInt, err := iat.Int64()
		require.NoError(t, err)
		expInt, err := exp.Int64()
		require.NoError(t, err)
		assert.Equal(t, expInt-iatInt, responder.ToMap()["expires_in"], "expires_in is exp - iat")
	})

	t.Run("minted result matches the claims", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, mintedIDJAG{
			Token: minted.Token, JTI: minted.JTI,
			IssuedAt: time.Unix(1_800_000_000, 0), ExpiresAt: time.Unix(1_800_000_060, 0),
		}, minted)
	})
}

func TestMintIDJAGActAndNotAfter(t *testing.T) {
	t.Parallel()

	signer := newMintTestSigner(newTestJWKS(t))
	m := mintTestMint(t)
	m.Act = &actClaim{Issuer: mintTestIssuer, Subject: "agent-1"}
	m.NotAfter = []time.Time{
		{},
		mintTestNow.Add(45*time.Second + 300*time.Millisecond),
		mintTestNow.Add(50 * time.Second),
	}
	responder := fosite.NewAccessResponse()

	minted, _, err := respondWithIDJAG(context.Background(), signer, allowAllIDJAGPolicy, m, responder, mintTestNow)
	require.NoError(t, err)

	payload := decodeJWTPart(t, minted.Token, 1)
	assert.Equal(t, map[string]any{"iss": mintTestIssuer, "sub": "agent-1"}, payload["act"])
	assert.Equal(t, json.Number("1800000045"), payload["exp"], "exp is the floor of the earliest non-zero cap")
	assert.Equal(t, int64(45), responder.ToMap()["expires_in"])
	assert.Equal(t, time.Unix(1_800_000_045, 0), minted.ExpiresAt)
}

func TestIDJAGExpiry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		notAfter []time.Time
		want     int64
	}{
		{name: "no caps: now plus lifetime, floored", want: 1_800_000_060},
		{name: "only a zero cap is ignored", notAfter: []time.Time{{}}, want: 1_800_000_060},
		{name: "a later cap never extends exp", notAfter: []time.Time{mintTestNow.Add(time.Hour)}, want: 1_800_000_060},
		{name: "an earlier cap wins, floored",
			notAfter: []time.Time{mintTestNow.Add(30*time.Second + 300*time.Millisecond)}, want: 1_800_000_030},
		{name: "the earliest of several caps wins",
			notAfter: []time.Time{mintTestNow.Add(time.Hour), {}, mintTestNow.Add(20 * time.Second), mintTestNow.Add(40 * time.Second)},
			want:     1_800_000_020},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, idJAGExpiry(mintTestNow, time.Minute, tt.notAfter))
		})
	}
}

func TestCheckIDJAGRequest(t *testing.T) {
	t.Parallel()

	target := mintTestTarget()
	policy := ConfiguredClientPolicy{IDJAGTargets: map[string]IDJAGTarget{target.Issuer: target}}
	validForm := func() url.Values {
		return url.Values{
			"subject_token":       {"subject"},
			"subject_token_type":  {oauthproto.TokenTypeRefreshToken},
			mintTestAudienceParam: {mintTestTargetIssuer},
			"resource":            {mintTestResource},
		}
	}
	wantShape := idJAGRequestShape{Target: target, Resource: mintTestResource}

	tests := []struct {
		name      string
		mutate    func(url.Values)
		wantCode  string // empty for success
		wantParam string // a substring of the hint naming the parameter
	}{
		{name: "valid request", mutate: func(url.Values) {}},
		{name: "empty first audience is a single audience",
			mutate: func(f url.Values) { f[mintTestAudienceParam] = []string{"", mintTestTargetIssuer} }},
		{name: "empty first resource is a single resource",
			mutate: func(f url.Values) { f["resource"] = []string{"", mintTestResource} }},
		{name: "unknown parameter is ignored", mutate: func(f url.Values) { f.Set("mecatl_operation", "x") }},
		{name: "empty actor_token alone is ignored", mutate: func(f url.Values) { f.Set("actor_token", "") }},
		{name: "empty scope is ignored", mutate: func(f url.Values) { f.Set("scope", "") }},
		{name: "empty authorization_details is ignored", mutate: func(f url.Values) { f.Set("authorization_details", "") }},

		{name: "missing subject_token", mutate: func(f url.Values) { f.Del("subject_token") },
			wantCode: "invalid_request", wantParam: "'subject_token'"},
		{name: "empty subject_token", mutate: func(f url.Values) { f.Set("subject_token", "") },
			wantCode: "invalid_request", wantParam: "'subject_token'"},
		{name: "missing audience", mutate: func(f url.Values) { f.Del(mintTestAudienceParam) },
			wantCode: "invalid_request", wantParam: "'audience'"},
		{name: "only empty audience values", mutate: func(f url.Values) { f[mintTestAudienceParam] = []string{"", ""} },
			wantCode: "invalid_request", wantParam: "'audience'"},
		{name: "repeated audience",
			mutate:   func(f url.Values) { f[mintTestAudienceParam] = []string{mintTestTargetIssuer, mintTestTargetIssuer} },
			wantCode: "invalid_target", wantParam: "'audience'"},
		{name: "missing resource", mutate: func(f url.Values) { f.Del("resource") },
			wantCode: "invalid_target", wantParam: "'resource'"},
		{name: "only empty resource values", mutate: func(f url.Values) { f["resource"] = []string{"", ""} },
			wantCode: "invalid_target", wantParam: "'resource'"},
		{name: "repeated resource",
			mutate:   func(f url.Values) { f["resource"] = []string{mintTestResource, "https://gw.example.com/other"} },
			wantCode: "invalid_target", wantParam: "'resource'"},
		{name: "XAA-compatibility gap: scope refused", mutate: func(f url.Values) { f.Set("scope", "read") },
			wantCode: "invalid_scope", wantParam: "'scope'"},
		{name: "XAA-compatibility gap: scope refused when it follows an empty value",
			mutate:   func(f url.Values) { f["scope"] = []string{"", "read"} },
			wantCode: "invalid_scope", wantParam: "'scope'"},
		{name: "XAA-compatibility gap: repeated scope refused", mutate: func(f url.Values) { f["scope"] = []string{"a", "b"} },
			wantCode: "invalid_scope", wantParam: "'scope'"},
		{name: "authorization_details refused", mutate: func(f url.Values) { f.Set("authorization_details", "[]") },
			wantCode: "invalid_authorization_details", wantParam: "'authorization_details'"},
		{name: "authorization_details refused when it follows an empty value",
			mutate:   func(f url.Values) { f["authorization_details"] = []string{"", "[]"} },
			wantCode: "invalid_authorization_details", wantParam: "'authorization_details'"},
		{name: "actor_token refused", mutate: func(f url.Values) { f.Set("actor_token", "actor") },
			wantCode: "invalid_request", wantParam: "'actor_token'"},
		{name: "actor_token refused when it follows an empty value",
			mutate:   func(f url.Values) { f["actor_token"] = []string{"", "actor"} },
			wantCode: "invalid_request", wantParam: "'actor_token'"},
		{name: "actor_token_type alone refused",
			mutate:   func(f url.Values) { f.Set("actor_token_type", oauthproto.TokenTypeJWT) },
			wantCode: "invalid_request", wantParam: "'actor_token_type'"},
		{name: "actor_token_type refused when it follows an empty value",
			mutate:   func(f url.Values) { f["actor_token_type"] = []string{"", oauthproto.TokenTypeJWT} },
			wantCode: "invalid_request", wantParam: "'actor_token_type'"},
		{name: "actor_token_type with an empty actor_token refused",
			mutate: func(f url.Values) {
				f.Set("actor_token", "")
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
			},
			wantCode: "invalid_request", wantParam: "'actor_token_type'"},
		{name: "order: missing subject_token wins over missing audience",
			mutate:   func(f url.Values) { f.Del("subject_token"); f.Del(mintTestAudienceParam) },
			wantCode: "invalid_request", wantParam: "'subject_token'"},
		{name: "order: missing audience wins over scope",
			mutate:   func(f url.Values) { f.Del(mintTestAudienceParam); f.Set("scope", "read") },
			wantCode: "invalid_request", wantParam: "'audience'"},
		{name: "order: repeated audience wins over repeated resource",
			mutate: func(f url.Values) {
				f[mintTestAudienceParam] = []string{mintTestTargetIssuer, mintTestTargetIssuer}
				f["resource"] = []string{mintTestResource, "https://gw.example.com/other"}
			},
			wantCode: "invalid_target", wantParam: "'audience'"},
		{name: "order: repeated resource wins over scope",
			mutate: func(f url.Values) {
				f["resource"] = []string{mintTestResource, "https://gw.example.com/other"}
				f.Set("scope", "read")
			},
			wantCode: "invalid_target", wantParam: "'resource'"},
		{name: "order: scope wins over authorization_details",
			mutate:   func(f url.Values) { f.Set("scope", "read"); f.Set("authorization_details", "[]") },
			wantCode: "invalid_scope", wantParam: "'scope'"},
		{name: "order: authorization_details wins over actor_token",
			mutate:   func(f url.Values) { f.Set("authorization_details", "[]"); f.Set("actor_token", "actor") },
			wantCode: "invalid_authorization_details", wantParam: "'authorization_details'"},
		{name: "order: actor_token wins over actor_token_type",
			mutate: func(f url.Values) {
				f.Set("actor_token", "actor")
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
			},
			wantCode: "invalid_request", wantParam: "'actor_token' parameter is not supported"},
		{name: "order: actor_token_type alone wins over an unknown audience",
			mutate: func(f url.Values) {
				f.Set("actor_token_type", oauthproto.TokenTypeJWT)
				f.Set(mintTestAudienceParam, "https://unknown.example.com")
			},
			wantCode: "invalid_request", wantParam: "'actor_token_type' parameter must not be given without"},
		{name: "order: scope wins over an unknown audience",
			mutate: func(f url.Values) {
				f.Set("scope", "read")
				f.Set(mintTestAudienceParam, "https://unknown.example.com")
			},
			wantCode: "invalid_scope", wantParam: "'scope'"},
		{name: "order: actor_token wins over a resource the target does not list",
			mutate: func(f url.Values) {
				f.Set("actor_token", "actor")
				f.Set("resource", "https://gw.example.com/elsewhere")
			},
			wantCode: "invalid_request", wantParam: "'actor_token'"},
		{name: "unknown target", mutate: func(f url.Values) { f.Set(mintTestAudienceParam, "https://unknown.example.com") },
			wantCode: "invalid_target", wantParam: "'audience'"},
		{name: "audience must match the configured issuer exactly",
			mutate:   func(f url.Values) { f.Set(mintTestAudienceParam, "HTTPS://AS2.Example.com:443/") },
			wantCode: "invalid_target", wantParam: "'audience'"},
		{name: "audience with a trailing slash is a different issuer",
			mutate:   func(f url.Values) { f.Set(mintTestAudienceParam, mintTestTargetIssuer+"/") },
			wantCode: "invalid_target", wantParam: "'audience'"},
		{name: "audience with a space is one unknown value",
			mutate:   func(f url.Values) { f.Set(mintTestAudienceParam, mintTestTargetIssuer+" https://x.example.com") },
			wantCode: "invalid_target", wantParam: "'audience'"},
		{name: "resource the target does not list",
			mutate:   func(f url.Values) { f.Set("resource", "https://gw.example.com/elsewhere") },
			wantCode: "invalid_target", wantParam: "'resource'"},
		{name: "resource must match exactly",
			mutate:   func(f url.Values) { f.Set("resource", mintTestResource+"/") },
			wantCode: "invalid_target", wantParam: "'resource'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			form := validForm()
			tt.mutate(form)

			shape, err := checkIDJAGRequest(form, policy)
			if tt.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, wantShape, shape)
				return
			}
			rfc := requireMintOAuthError(t, err, tt.wantCode)
			assert.Contains(t, rfc.GetDescription(), tt.wantParam)
			assert.Equal(t, idJAGRequestShape{}, shape)
		})
	}
}

// TestCheckIDJAGRequestOtherClientTarget pins that a target configured only for
// another client is refused exactly like an unknown target.
func TestCheckIDJAGRequestOtherClientTarget(t *testing.T) {
	t.Parallel()

	own := mintTestTarget()
	other := mintTestTarget()
	other.Issuer = mintTestOtherClient
	policies := map[string]ConfiguredClientPolicy{
		"client-a": {IDJAGTargets: map[string]IDJAGTarget{own.Issuer: own}},
		"client-b": {IDJAGTargets: map[string]IDJAGTarget{other.Issuer: other}},
	}
	refusal := func(audience string) []byte {
		form := url.Values{
			"subject_token": {"subject"}, mintTestAudienceParam: {audience}, "resource": {mintTestResource},
		}
		_, err := checkIDJAGRequest(form, policies["client-a"])
		rfc := requireMintOAuthError(t, err, "invalid_target")
		body, err := json.Marshal(rfc)
		require.NoError(t, err)
		return body
	}

	assert.Equal(t, string(refusal("https://unknown.example.com")), string(refusal(mintTestOtherClient)))
}

func TestSelectSubjectClaims(t *testing.T) {
	t.Parallel()

	available := sessionSubjectClaims(t)
	withoutKey := func(key string) map[string]json.RawMessage {
		out := maps.Clone(available)
		delete(out, key)
		return out
	}

	tests := []struct {
		name       string
		required   []string
		available  map[string]json.RawMessage
		wantKeys   []string
		wantReason string
		wantCode   string
	}{
		{name: "both claims selected", required: []string{"upstream_subject", "sub_id"}, available: available,
			wantKeys: []string{claimnames.UpstreamSubjectKey, claimnames.SubjectIdentifierKey}},
		{name: "unlisted claims are not copied", required: []string{"upstream_subject"}, available: available,
			wantKeys: []string{claimnames.UpstreamSubjectKey}},
		{name: "nothing required selects nothing", available: available, wantKeys: []string{}},
		{name: "upstream_subject missing", required: []string{"upstream_subject"},
			available: withoutKey(claimnames.UpstreamSubjectKey), wantReason: "upstream_subject_missing", wantCode: "invalid_request"},
		{name: "sub_id missing", required: []string{"upstream_subject", "sub_id"},
			available: withoutKey(claimnames.SubjectIdentifierKey), wantReason: "sub_id_missing", wantCode: "invalid_request"},
		{name: "a JSON null counts as missing", required: []string{"sub_id"},
			available: map[string]json.RawMessage{claimnames.SubjectIdentifierKey: json.RawMessage("null")}, wantReason: "sub_id_missing",
			wantCode: "invalid_request"},
		{name: "an empty value counts as missing", required: []string{"sub_id"},
			available: map[string]json.RawMessage{claimnames.SubjectIdentifierKey: {}}, wantReason: "sub_id_missing",
			wantCode: "invalid_request"},
		{name: "a whitespace-only value counts as missing", required: []string{"sub_id"},
			available: map[string]json.RawMessage{claimnames.SubjectIdentifierKey: json.RawMessage(" \t\n")}, wantReason: "sub_id_missing",
			wantCode: "invalid_request"},
		{name: "unknown configuration token is an internal failure", required: []string{"email"},
			available: available, wantCode: "server_error", wantReason: refuseInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			target := mintTestTarget()
			target.RequiredSubjectClaims = tt.required

			selected, reason, err := selectSubjectClaims(target, tt.available)
			assert.Equal(t, tt.wantReason, reason)
			if tt.wantCode != "" {
				if tt.wantCode == "invalid_request" {
					rfc := requireMintOAuthError(t, err, tt.wantCode)
					assert.Equal(t, tt.wantReason, rfc.GetDescription(), "a stable reason is the whole description")
				} else {
					requireMintFailure(t, err)
				}
				assert.Nil(t, selected)
				return
			}
			require.NoError(t, err)
			assert.ElementsMatch(t, tt.wantKeys, slices.Collect(maps.Keys(selected)))
			for key, value := range selected {
				assert.JSONEq(t, string(available[key]), string(value))
			}
		})
	}
}

// TestMintIDJAGGuards drives mintIDJAG directly. It covers checkIDJAGMint, the
// expiry check, and the claim-set checks on hand-built selections.
func TestMintIDJAGGuards(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*idJAGMint)
		selected  map[string]json.RawMessage // nil means the claims the test target requires
		wantCause string                     // empty when the mint succeeds
	}{
		{name: "the upstream subject key is allowed", selected: claimOf(claimnames.UpstreamSubjectKey, `{"iss":"x"}`)},
		{name: "sub_id is allowed", selected: claimOf(claimnames.SubjectIdentifierKey, `{"format":"opaque","id":"x"}`)},
		{name: "no subject claims", selected: map[string]json.RawMessage{}},
		{name: "nbf is reserved", selected: claimOf("nbf", `{}`), wantCause: `"nbf" is reserved`},
		{name: "cnf is reserved", selected: claimOf("cnf", `{}`), wantCause: `"cnf" is reserved`},
		{name: "iss is reserved", selected: claimOf("iss", `{}`), wantCause: `"iss" is reserved`},
		{name: "aud_tenant is reserved", selected: claimOf("aud_tenant", `{}`), wantCause: `"aud_tenant" is reserved`},
		{name: "scope is reserved", selected: claimOf("scope", `{}`), wantCause: `"scope" is reserved`},
		{name: "a claim outside the allowlist", selected: claimOf("email", `{}`), wantCause: `"email" is not allowed`},
		{name: "empty sub", mutate: func(m *idJAGMint) { m.Subject.Subject = "" }, wantCause: "empty subject"},
		{name: "empty client_id", mutate: func(m *idJAGMint) { m.Target.ClientID = "" }, wantCause: "empty client_id"},
		{name: "empty issuer", mutate: func(m *idJAGMint) { m.Issuer = "" }, wantCause: "empty issuer"},
		{name: "empty target issuer", mutate: func(m *idJAGMint) { m.Target.Issuer = "" }, wantCause: "empty target issuer"},
		{name: "empty resource", mutate: func(m *idJAGMint) { m.Resource = "" }, wantCause: "empty resource"},
		{name: "act without a subject", mutate: func(m *idJAGMint) { m.Act = &actClaim{Issuer: mintTestIssuer} },
			wantCause: "incomplete act"},
		{name: "act without an issuer", mutate: func(m *idJAGMint) { m.Act = &actClaim{Subject: "agent-1"} },
			wantCause: "incomplete act"},
		{name: "zero lifetime", mutate: func(m *idJAGMint) { m.Target.Lifetime = 0 }, wantCause: "exp is not after now"},
		{name: "empty claim value", selected: rawClaim(json.RawMessage{}), wantCause: `"sub_id" is empty or null`},
		{name: "null claim value", selected: rawClaim(json.RawMessage("null")), wantCause: `"sub_id" is empty or null`},
		{name: "whitespace-only claim value", selected: rawClaim(json.RawMessage(" \t\n")),
			wantCause: `"sub_id" is empty or null`},
		{name: "invalid JSON claim value", selected: rawClaim(json.RawMessage("{")),
			wantCause: `"sub_id" is not valid JSON`},
		{name: "string claim value", selected: rawClaim(json.RawMessage(`"x"`)),
			wantCause: `"sub_id" is not a JSON object`},
		{name: "number claim value", selected: rawClaim(json.RawMessage(`1`)),
			wantCause: `"sub_id" is not a JSON object`},
		{name: "array claim value", selected: rawClaim(json.RawMessage(`[{}]`)),
			wantCause: `"sub_id" is not a JSON object`},
		{name: "object claim value after whitespace", selected: rawClaim(json.RawMessage(" \n{}"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			signer := newMintTestSigner(newTestJWKS(t))
			m := mintTestMint(t)
			if tt.mutate != nil {
				tt.mutate(&m)
			}
			selected := tt.selected
			if selected == nil {
				selected = mustSelectClaims(t, m)
			}

			minted, err := mintIDJAG(context.Background(), signer, m, selected, mintTestNow)
			if tt.wantCause == "" {
				require.NoError(t, err)
				assert.NotEmpty(t, minted.Token)
				return
			}
			requireMintFailure(t, err)
			assert.ErrorContains(t, errors.Unwrap(unwrapOAuth(t, err)), tt.wantCause)
			assert.Equal(t, mintedIDJAG{}, minted)
			assert.Zero(t, signer.calls, "nothing is signed when a guard fails")
		})
	}
}

// TestRespondWithIDJAGElapsedBound covers the expiry refusals made before
// signing: a cap that has elapsed is the client's expired grant, an
// invalid_grant with reasonBoundElapsed, and a lifetime that cannot reach the
// next second is a server_error.
func TestRespondWithIDJAGElapsedBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*idJAGMint)
		wantReason string
		wantCode   string
	}{
		{name: "exp equal to now",
			mutate:     func(m *idJAGMint) { m.NotAfter = []time.Time{mintTestNow} },
			wantReason: reasonBoundElapsed, wantCode: "invalid_grant"},
		{name: "exp floored back to now's second",
			mutate:     func(m *idJAGMint) { m.NotAfter = []time.Time{mintTestNow.Add(400 * time.Millisecond)} },
			wantReason: reasonBoundElapsed, wantCode: "invalid_grant"},
		{name: "cap less than one second away",
			mutate:     func(m *idJAGMint) { m.NotAfter = []time.Time{mintTestNow.Add(499 * time.Millisecond)} },
			wantReason: reasonBoundElapsed, wantCode: "invalid_grant"},
		{name: "cap in the past",
			mutate:     func(m *idJAGMint) { m.NotAfter = []time.Time{mintTestNow.Add(-time.Hour)} },
			wantReason: reasonBoundElapsed, wantCode: "invalid_grant"},
		{name: "one elapsed cap among open ones",
			mutate: func(m *idJAGMint) {
				m.NotAfter = []time.Time{mintTestNow.Add(time.Hour), {}, mintTestNow.Add(-time.Second)}
			},
			wantReason: reasonBoundElapsed, wantCode: "invalid_grant"},
		{name: "zero lifetime is an operator fault",
			mutate:     func(m *idJAGMint) { m.Target.Lifetime = 0 },
			wantReason: refuseInternal, wantCode: "server_error"},
		{name: "sub-second lifetime is an operator fault",
			mutate:     func(m *idJAGMint) { m.Target.Lifetime = 400 * time.Millisecond },
			wantReason: refuseInternal, wantCode: "server_error"},
		{name: "zero lifetime with an elapsed cap is an operator fault",
			mutate: func(m *idJAGMint) {
				m.Target.Lifetime = 0
				m.NotAfter = []time.Time{mintTestNow.Add(-time.Hour)}
			},
			wantReason: refuseInternal, wantCode: "server_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			signer := newMintTestSigner(newTestJWKS(t))
			responder := fosite.NewAccessResponse()
			m := mintTestMint(t)
			tt.mutate(&m)

			minted, reason, err := respondWithIDJAG(context.Background(), signer, allowAllIDJAGPolicy, m, responder, mintTestNow)

			require.Error(t, err)
			assert.Equal(t, tt.wantReason, reason)
			if tt.wantCode == "server_error" {
				requireMintFailure(t, err)
			} else {
				rfc := requireMintOAuthError(t, err, tt.wantCode)
				assert.Equal(t, reasonBoundElapsed, rfc.GetDescription(), "the stable reason is the whole description")
			}
			assert.Equal(t, mintedIDJAG{}, minted)
			assert.Zero(t, signer.calls, "nothing is signed")
			assert.Empty(t, responder.ToMap()["access_token"])
		})
	}

	t.Run("a cap one second away still mints, with exp at the cap", func(t *testing.T) {
		t.Parallel()
		signer := newMintTestSigner(newTestJWKS(t))
		m := mintTestMint(t)
		m.NotAfter = []time.Time{time.Unix(mintTestNow.Unix()+1, 0)}
		minted, _, err := respondWithIDJAG(
			context.Background(), signer, allowAllIDJAGPolicy, m, fosite.NewAccessResponse(), mintTestNow)
		require.NoError(t, err)
		assert.Equal(t, mintTestNow.Unix()+1, minted.ExpiresAt.Unix())
	})
}

func mustSelectClaims(t *testing.T, m idJAGMint) map[string]json.RawMessage {
	t.Helper()
	selected, reason, err := selectSubjectClaims(m.Target, m.Subject.Claims)
	require.NoError(t, err)
	require.Empty(t, reason)
	return selected
}

func rawClaim(value json.RawMessage) map[string]json.RawMessage {
	return map[string]json.RawMessage{claimnames.SubjectIdentifierKey: value}
}

func claimOf(name, value string) map[string]json.RawMessage {
	return map[string]json.RawMessage{name: json.RawMessage(value)}
}

// TestIDJAGMintClaimSets pins the subject-claim allowlist and the reserved
// names, so a change to either fails here rather than quietly widening what a
// minted ID-JAG may carry.
func TestIDJAGMintClaimSets(t *testing.T) {
	t.Parallel()

	assert.Equal(t, map[string]idJAGSubjectClaim{
		"upstream_subject": {wireKey: "https://toolhive.dev/upstream_subject", missingReason: "upstream_subject_missing"},
		"sub_id":           {wireKey: "sub_id", missingReason: "sub_id_missing"},
	}, idJAGSubjectClaims)
	assert.ElementsMatch(t, []string{
		"iss", "sub", "aud", "client_id", "jti", "iat", "exp", "nbf", "resource", "scope",
		"authorization_details", "act", "cnf", "tenant", "aud_tenant",
	}, slices.Collect(maps.Keys(idJAGReservedClaims)))
	assert.Equal(t, "policy_denied", reasonPolicyDenied)
}

func TestRespondWithIDJAGPolicy(t *testing.T) {
	t.Parallel()

	deny := func(context.Context, idJAGPolicyInput) error { return errIDJAGPolicyDenied }
	wrappedDenyErr := fmt.Errorf("target not allowed for agent: %w", errIDJAGPolicyDenied)
	wrappedDeny := func(context.Context, idJAGPolicyInput) error { return wrappedDenyErr }
	broken := func(context.Context, idJAGPolicyInput) error { return errors.New("policy store unavailable") }

	tests := []struct {
		name       string
		policy     idJAGPolicy
		policyErr  error
		wantCode   string // empty when the token is minted
		wantReason string
	}{
		{name: "nil policy is an internal failure", wantCode: "server_error", wantReason: refuseInternal},
		{name: "allow-all policy", policy: allowAllIDJAGPolicy},
		{name: "allowing policy", policy: func(context.Context, idJAGPolicyInput) error { return nil }},
		{name: "denial", policy: deny, policyErr: errIDJAGPolicyDenied, wantCode: "invalid_request", wantReason: "policy_denied"},
		{name: "wrapped denial", policy: wrappedDeny, policyErr: wrappedDenyErr, wantCode: "invalid_request", wantReason: "policy_denied"},
		{name: "policy failure", policy: broken, wantCode: "server_error", wantReason: refuseInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			signer := newMintTestSigner(newTestJWKS(t))
			responder := fosite.NewAccessResponse()

			minted, reason, err := respondWithIDJAG(context.Background(), signer, tt.policy, mintTestMint(t), responder, mintTestNow)
			assert.Equal(t, tt.wantReason, reason)
			if tt.wantCode == "" {
				require.NoError(t, err)
				assert.Equal(t, 1, signer.calls)
				assert.Equal(t, minted.Token, responder.GetAccessToken())
				return
			}
			rfc := requireMintOAuthError(t, err, tt.wantCode)
			if tt.wantCode == "invalid_request" {
				assert.Equal(t, tt.wantReason, rfc.GetDescription(), "a stable reason is the whole description")
				body, marshalErr := json.Marshal(rfc)
				require.NoError(t, marshalErr)
				assert.JSONEq(t, `{"error":"invalid_request","error_description":"policy_denied"}`, string(body),
					"the policy's own error never reaches the client")
				assert.ErrorIs(t, err, errIDJAGPolicyDenied)
				assert.ErrorIs(t, err, tt.policyErr, "the policy's own error is the cause")
			} else {
				requireMintFailure(t, err)
			}
			assert.Zero(t, signer.calls, "the policy runs before signing")
			assert.Equal(t, mintedIDJAG{}, minted)
			assert.Empty(t, responder.ToMap()["access_token"])
		})
	}
}

func TestRespondWithIDJAGPolicyInput(t *testing.T) {
	t.Parallel()

	m := mintTestMint(t)
	m.Act = &actClaim{Issuer: mintTestIssuer, Subject: "agent-1"}
	var got idJAGPolicyInput
	policy := func(_ context.Context, in idJAGPolicyInput) error {
		got = in
		// A policy that changes its input must not change what is minted.
		in.Subject.Claims[claimnames.SubjectIdentifierKey] = json.RawMessage(`{"changed":true}`)
		in.Target.Resources[0] = "https://changed.example.com"
		in.Act.Subject = "changed"
		return nil
	}

	minted, _, err := respondWithIDJAG(
		context.Background(), newMintTestSigner(newTestJWKS(t)), policy, m, fosite.NewAccessResponse(), mintTestNow)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{claimnames.UpstreamSubjectKey, claimnames.SubjectIdentifierKey}, slices.Collect(maps.Keys(got.Subject.Claims)),
		"the policy sees only the selected claims")
	assert.Equal(t, mintTestRequesting, got.ClientID, "the policy sees the client authenticated here")
	assert.Equal(t, mintTestSubject, got.Subject.Subject)
	assert.Equal(t, mintTestResource, got.Resource)
	assert.Equal(t, mintTestTargetClient, got.Target.ClientID)

	payload := decodeJWTPart(t, minted.Token, 1)
	assert.Equal(t, mintTestTargetClient, payload["client_id"], "the token names the client at the target")
	assert.Equal(t, map[string]any{"format": "iss_sub", "iss": mintTestUpstreamIss, "sub": mintTestUpstreamSub},
		payload[claimnames.SubjectIdentifierKey], "the policy's edit does not reach the token")
	assert.Equal(t, map[string]any{"iss": mintTestIssuer, "sub": "agent-1"}, payload["act"])
	assert.Equal(t, mintTestResource, m.Target.Resources[0], "the caller's target is not changed")
}

func TestRespondWithIDJAGPolicyInPlaceEdit(t *testing.T) {
	t.Parallel()

	m := mintTestMint(t)
	want := decodeJWTPart(t, mustMintToken(t, m), 1)[claimnames.SubjectIdentifierKey]
	policy := func(_ context.Context, in idJAGPolicyInput) error {
		raw := in.Subject.Claims[claimnames.SubjectIdentifierKey]
		copy(raw[bytes.Index(raw, []byte("iss_sub")):], "XXXXXXX")
		return nil
	}

	minted, _, err := respondWithIDJAG(
		context.Background(), newMintTestSigner(newTestJWKS(t)), policy, m, fosite.NewAccessResponse(), mintTestNow)
	require.NoError(t, err)

	assert.Equal(t, want, decodeJWTPart(t, minted.Token, 1)[claimnames.SubjectIdentifierKey], "an in-place edit by the policy changes nothing minted")
}

func mustMintToken(t *testing.T, m idJAGMint) string {
	t.Helper()
	minted, _, err := respondWithIDJAG(
		context.Background(), newMintTestSigner(newTestJWKS(t)), allowAllIDJAGPolicy, m, fosite.NewAccessResponse(), mintTestNow)
	require.NoError(t, err)
	return minted.Token
}

func TestRespondWithIDJAGFailures(t *testing.T) {
	t.Parallel()

	t.Run("signing failure", func(t *testing.T) {
		t.Parallel()
		signer := &recordingSigner{err: errors.New("kms unavailable")}
		responder := fosite.NewAccessResponse()

		_, reason, err := respondWithIDJAG(context.Background(), signer, allowAllIDJAGPolicy, mintTestMint(t), responder, mintTestNow)
		assert.Equal(t, refuseInternal, reason)
		requireMintFailure(t, err)
		assert.ErrorContains(t, errors.Unwrap(unwrapOAuth(t, err)), "kms unavailable")
		assert.Empty(t, responder.ToMap()["access_token"])
	})

	t.Run("jti is a fresh UUID each time", func(t *testing.T) {
		t.Parallel()
		signer := newMintTestSigner(newTestJWKS(t))
		m := mintTestMint(t)

		first, _, err := respondWithIDJAG(context.Background(), signer, allowAllIDJAGPolicy, m, fosite.NewAccessResponse(), mintTestNow)
		require.NoError(t, err)
		second, _, err := respondWithIDJAG(context.Background(), signer, allowAllIDJAGPolicy, m, fosite.NewAccessResponse(), mintTestNow)
		require.NoError(t, err)
		assert.Len(t, first.JTI, 36)
		assert.NotEqual(t, first.JTI, second.JTI)
		assert.Equal(t, first.JTI, decodeJWTPart(t, first.Token, 1)["jti"])
	})
}

func TestRespondWithIDJAGMissingClaim(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		available  map[string]json.RawMessage
		required   []string
		wantCode   string
		wantReason string
	}{
		{name: "upstream_subject missing", required: []string{"upstream_subject", "sub_id"},
			available: claimOf(claimnames.SubjectIdentifierKey, `{"format":"opaque","id":"x"}`),
			wantCode:  "invalid_request", wantReason: "upstream_subject_missing"},
		{name: "sub_id null", required: []string{"sub_id"}, available: rawClaim(json.RawMessage("null")),
			wantCode: "invalid_request", wantReason: "sub_id_missing"},
		{name: "unknown required token", required: []string{"email"}, available: sessionSubjectClaims(t),
			wantCode: "server_error", wantReason: refuseInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			signer := newMintTestSigner(newTestJWKS(t))
			responder := fosite.NewAccessResponse()
			m := mintTestMint(t)
			m.Target.RequiredSubjectClaims = tt.required
			m.Subject.Claims = tt.available
			policy := func(context.Context, idJAGPolicyInput) error {
				t.Error("the policy must not run when a required claim is missing")
				return nil
			}

			minted, reason, err := respondWithIDJAG(context.Background(), signer, policy, m, responder, mintTestNow)
			assert.Equal(t, tt.wantReason, reason)
			rfc := requireMintOAuthError(t, err, tt.wantCode)
			if tt.wantCode == "invalid_request" {
				assert.Equal(t, tt.wantReason, rfc.GetDescription(), "a stable reason is the whole description")
			} else {
				requireMintFailure(t, err)
			}
			assert.Equal(t, mintedIDJAG{}, minted)
			assert.Zero(t, signer.calls, "nothing is signed")
			assert.Empty(t, responder.ToMap()["access_token"])
		})
	}
}
