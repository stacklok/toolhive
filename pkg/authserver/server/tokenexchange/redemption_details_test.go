// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authserver/authorizationdetails"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
	"github.com/stacklok/toolhive/pkg/authserver/subject/mocks"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	testCallType       = "https://example.com/call"
	testCallEntry      = `{"type":"` + testCallType + `","note":"first","limit":1}`
	testOtherCallEntry = `{"type":"` + testCallType + `","note":"second","limit":1}`
	// bigEntry holds an integer above 2^53, which a float64 would round.
	bigEntry = `{"type":"` + testCallType + `","note":"big","limit":9007199254740993}`
	// surrogateEntry holds a lone surrogate escape, which decodes to U+FFFD.
	surrogateEntry = `{"type":"` + testCallType + `","note":"\ud800","limit":1}`

	testOtherType  = "https://example.com/other-call"
	testOtherEntry = `{"type":"` + testOtherType + `","note":"other"}`

	hintDetailsClaimShape = "invalid_assertion: The ID-JAG assertion authorization_details claim must be a non-empty array of objects."
	// hintDetailsParamRepeated is the description of a repeated parameter.
	hintDetailsParamRepeated = "The authorization_details parameter must not be repeated."
)

// detailsProcessingFailed is what the client sees for a hook failure or a hook
// that returned an entry the ID-JAG did not sign.
var detailsProcessingFailed = fosite.ErrServerError.
	WithDescription("The authorization_details could not be processed.").GetDescription()

type grantHook = func(context.Context, authorizationdetails.Request) (authorizationdetails.Result, error)

// newCallDetailsRegistry registers testCallType for the JWT-bearer grant with
// the given hook.
func newCallDetailsRegistry(t *testing.T, hook grantHook) *authorizationdetails.Registry {
	t.Helper()
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testCallType: {GrantTypes: []string{oauthproto.GrantTypeJWTBearer}, Grant: hook},
	})
	require.NoError(t, err)
	return registry
}

// singleEntryHook accepts exactly one entry that carries a non-empty "note",
// like a call type would, and refuses the rest with a hook reason. When seen
// is not nil it stores the request it received there.
func singleEntryHook(seen *authorizationdetails.Request) grantHook {
	return func(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
		if seen != nil {
			*seen = req
		}
		refuse := func(reason string) error {
			return authorizationdetails.Error{Code: authorizationdetails.CodeInvalidAuthorizationDetails, Reason: reason}
		}
		if len(req.Entries) != 1 {
			return authorizationdetails.Result{}, refuse("single_entry_required")
		}
		var entry struct {
			Note string `json:"note"`
		}
		if err := json.Unmarshal(req.Entries[0], &entry); err != nil || entry.Note == "" {
			return authorizationdetails.Result{}, refuse("note_required")
		}
		return authorizationdetails.Result{Entries: req.Entries}, nil
	}
}

// grantingHook returns the given entries whatever it was asked. With no
// entries it drops all of its type's entries.
func grantingHook(entries ...string) grantHook {
	return func(context.Context, authorizationdetails.Request) (authorizationdetails.Result, error) {
		result := authorizationdetails.Result{}
		for _, entry := range entries {
			result.Entries = append(result.Entries, json.RawMessage(entry))
		}
		return result, nil
	}
}

// keepAllHook keeps every entry it receives.
func keepAllHook(_ context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
	return authorizationdetails.Result{Entries: req.Entries}, nil
}

// newTwoTypeDetailsRegistry registers testCallType with callHook and
// testOtherType with otherHook, both for the JWT-bearer grant.
func newTwoTypeDetailsRegistry(t *testing.T, callHook, otherHook grantHook) *authorizationdetails.Registry {
	t.Helper()
	registry, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testCallType:  {GrantTypes: []string{oauthproto.GrantTypeJWTBearer}, Grant: callHook},
		testOtherType: {GrantTypes: []string{oauthproto.GrantTypeJWTBearer}, Grant: otherHook},
	})
	require.NoError(t, err)
	return registry
}

// failingHook fails with an internal error whose text must not reach the client.
func failingHook(context.Context, authorizationdetails.Request) (authorizationdetails.Result, error) {
	return authorizationdetails.Result{}, errors.New("private hook detail")
}

// detailsClaims returns valid ID-JAG claims that carry raw as their
// authorization_details claim, in both the decoded claim set and the raw
// bytes, as the validator produces them.
func detailsClaims(t *testing.T, raw string) *ValidatedClaims {
	t.Helper()
	return withDetails(t, validIDJAGClaims(), raw)
}

// withDetails adds raw to claims as their authorization_details claim.
func withDetails(t *testing.T, claims *ValidatedClaims, raw string) *ValidatedClaims {
	t.Helper()
	var decoded any
	require.NoError(t, json.Unmarshal([]byte(raw), &decoded))
	claims.Extra[authorizationDetailsParam] = decoded
	claims.Claims = maps.Clone(claims.Extra)
	claims.AuthorizationDetails = json.RawMessage(raw)
	return claims
}

// redeemIDJAG runs the full ID-JAG handler and, when it accepts, the response
// step. It returns the request, the response and the consumer.
func redeemIDJAG(
	t *testing.T, claims *ValidatedClaims, registry *authorizationdetails.Registry, form url.Values,
) (*fosite.AccessRequest, *fosite.AccessResponse, *recordingAssertionConsumer, error) {
	t.Helper()
	consumer := &recordingAssertionConsumer{}
	h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: claims}, consumer)
	h.detailsRegistry = registry
	req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
	maps.Copy(req.Form, form)
	if err := h.HandleTokenEndpointRequest(context.Background(), req); err != nil {
		return req, nil, consumer, err
	}
	resp := fosite.NewAccessResponse()
	require.NoError(t, h.PopulateTokenEndpointResponse(context.Background(), req, resp))
	return req, resp, consumer, nil
}

// TestIDJAGRedemptionDetailsClaim covers each row of the redemption error
// table on the ID-JAG grant. Every refusal leaves the ID-JAG unconsumed. On
// success the token's claim and the response member hold the kept entries
// with the bytes the issuer signed.
func TestIDJAGRedemptionDetailsClaim(t *testing.T) {
	t.Parallel()

	accept := singleEntryHook(nil)
	exchangeOnly, err := authorizationdetails.New(map[string]authorizationdetails.Type{
		testCallType: {GrantTypes: []string{oauthproto.GrantTypeTokenExchange}, Grant: accept},
	})
	require.NoError(t, err)
	// The call type drops its entry and the other type keeps its own.
	oneTypeDropped := newTwoTypeDetailsRegistry(t, grantingHook(), keepAllHook)
	everyTypeDropped := newTwoTypeDetailsRegistry(t, grantingHook(), grantingHook())

	tests := []struct {
		name string
		// details is the ID-JAG's authorization_details claim, absent when empty.
		details string
		// mutate changes the claims after details is added.
		mutate func(*ValidatedClaims)
		// hook is the registered type's hook. With neither hook nor registry
		// the handler has no registry.
		hook     grantHook
		registry *authorizationdetails.Registry
		form     url.Values
		// wantCode is the refusal's error code, empty for success.
		wantCode    string
		wantStatus  int // defaults to 400
		wantDesc    string
		wantDetails string // the granted entries, empty when none
	}{
		{name: "no claim is redeemed without details"},
		{name: "one entry is granted unchanged", details: "[" + testCallEntry + "]", hook: accept,
			wantDetails: "[" + testCallEntry + "]"},
		{name: "an integer above 2^53 keeps every digit", details: "[" + bigEntry + "]", hook: accept,
			wantDetails: "[" + bigEntry + "]"},
		{name: "claim in the full claim set only is processed", details: "[" + testCallEntry + "]", hook: accept,
			mutate:      func(c *ValidatedClaims) { delete(c.Extra, authorizationDetailsParam) },
			wantDetails: "[" + testCallEntry + "]"},
		{name: "claim in Extra only is processed", details: "[" + testCallEntry + "]", hook: accept,
			mutate:      func(c *ValidatedClaims) { c.Claims = nil },
			wantDetails: "[" + testCallEntry + "]"},

		// The claim is not a non-empty array of objects.
		{name: "null", details: "null", wantCode: "invalid_grant", wantDesc: hintDetailsClaimShape},
		{name: "empty array", details: "[]", hook: accept, wantCode: "invalid_grant", wantDesc: hintDetailsClaimShape},
		{name: "string", details: `"x"`, wantCode: "invalid_grant", wantDesc: hintDetailsClaimShape},
		{name: "object", details: testCallEntry, wantCode: "invalid_grant", wantDesc: hintDetailsClaimShape},
		{name: "array holding a non-object", details: "[" + testCallEntry + `,"x"]`,
			wantCode: "invalid_grant", wantDesc: hintDetailsClaimShape},
		{name: "claim present without its raw bytes fails closed", details: "[" + testCallEntry + "]", hook: accept,
			mutate:   func(c *ValidatedClaims) { c.AuthorizationDetails = nil },
			wantCode: "invalid_grant", wantDesc: hintDetailsClaimShape},

		// Types and shapes.
		{name: "no registry", details: "[" + testCallEntry + "]",
			wantCode: "invalid_authorization_details", wantDesc: "unknown_type"},
		{name: "no registry and an entry without a type", details: `[{"note":"a"}]`,
			wantCode: "invalid_authorization_details", wantDesc: "unknown_type"},
		{name: "type registered for token exchange only", details: "[" + testCallEntry + "]", registry: exchangeOnly,
			wantCode: "invalid_authorization_details", wantDesc: "unknown_type"},
		{name: "unregistered type", details: `[{"type":"https://example.com/other"}]`, hook: accept,
			wantCode: "invalid_authorization_details", wantDesc: "unknown_type"},
		{name: "member repeated in another letter case", details: `[{"type":"` + testCallType + `","note":"a","Note":"b"}]`,
			hook: accept, wantCode: "invalid_authorization_details", wantDesc: "malformed_entry"},
		{name: "entry without a type", details: `[{"note":"a"}]`, hook: accept,
			wantCode: "invalid_authorization_details", wantDesc: "malformed_entry"},

		// The hook's own refusals pass through.
		{name: "two entries", details: "[" + testCallEntry + "," + testOtherCallEntry + "]", hook: accept,
			wantCode: "invalid_authorization_details", wantDesc: "single_entry_required"},
		{name: "the same entry twice", details: "[" + testCallEntry + "," + testCallEntry + "]", hook: accept,
			wantCode: "invalid_authorization_details", wantDesc: "single_entry_required"},
		{name: "hook refuses the entry", details: `[{"type":"` + testCallType + `"}]`, hook: accept,
			wantCode: "invalid_authorization_details", wantDesc: "note_required"},
		{name: "hook fails", details: "[" + testCallEntry + "]", hook: failingHook,
			wantCode: "server_error", wantStatus: http.StatusInternalServerError, wantDesc: detailsProcessingFailed},

		// The output check: a hook may only drop whole signed entries.
		{name: "hook changes the entry", details: "[" + testCallEntry + "]", hook: grantingHook(testOtherCallEntry),
			wantCode: "server_error", wantStatus: http.StatusInternalServerError, wantDesc: detailsProcessingFailed},
		{name: "hook adds an entry", details: "[" + testCallEntry + "]",
			hook:     grantingHook(testCallEntry, testOtherCallEntry),
			wantCode: "server_error", wantStatus: http.StatusInternalServerError, wantDesc: detailsProcessingFailed},
		{name: "hook rewrites 1 as 1.0", details: "[" + testCallEntry + "]",
			hook:     grantingHook(strings.Replace(testCallEntry, `"limit":1`, `"limit":1.0`, 1)),
			wantCode: "server_error", wantStatus: http.StatusInternalServerError, wantDesc: detailsProcessingFailed},
		{name: "hook rounds an integer above 2^53", details: "[" + bigEntry + "]",
			hook:     grantingHook(strings.Replace(bigEntry, "9007199254740993", "9007199254740992", 1)),
			wantCode: "server_error", wantStatus: http.StatusInternalServerError, wantDesc: detailsProcessingFailed},
		{name: "hook grants none of its entries", details: "[" + testCallEntry + "]", hook: grantingHook(),
			wantCode: "invalid_authorization_details", wantDesc: "empty_grant"},
		{name: "hook drops one of two entries", details: "[" + testCallEntry + "," + testOtherCallEntry + "]",
			hook: grantingHook(testOtherCallEntry), wantDetails: "[" + testOtherCallEntry + "]"},
		{name: "hook reorders members: the token carries the signed bytes", details: "[" + testCallEntry + "]",
			hook:        grantingHook(`{"limit":1,"note":"first","type":"` + testCallType + `"}`),
			wantDetails: "[" + testCallEntry + "]"},
		{name: "lone surrogate: the token carries the signed bytes, not the hook's", details: "[" + surrogateEntry + "]",
			hook:        grantingHook(strings.Replace(surrogateEntry, `\ud800`, `\ufffd`, 1)),
			wantDetails: "[" + surrogateEntry + "]"},
		{name: "one type drops all its entries and the other type's entry is granted",
			details: "[" + testCallEntry + "," + testOtherEntry + "]", registry: oneTypeDropped,
			wantDetails: "[" + testOtherEntry + "]"},
		{name: "every type drops all its entries", details: "[" + testCallEntry + "," + testOtherEntry + "]",
			registry: everyTypeDropped, wantCode: "invalid_authorization_details", wantDesc: "empty_grant"},

		// The redemption parameter.
		{name: "parameter", form: url.Values{authorizationDetailsParam: {"[" + testCallEntry + "]"}}, hook: accept,
			wantCode: "invalid_authorization_details", wantDesc: "parameter_not_allowed"},
		{name: "parameter alongside a valid claim", details: "[" + testCallEntry + "]", hook: accept,
			form:     url.Values{authorizationDetailsParam: {"[" + testCallEntry + "]"}},
			wantCode: "invalid_authorization_details", wantDesc: "parameter_not_allowed"},
		{name: "parameter repeated empty", form: url.Values{authorizationDetailsParam: {"", ""}},
			wantCode: "invalid_request", wantDesc: hintDetailsParamRepeated},
		{name: "parameter repeated with values", form: url.Values{authorizationDetailsParam: {"[]", "[]"}},
			wantCode: "invalid_request", wantDesc: hintDetailsParamRepeated},
		{name: "parameter as one empty value is absent", form: url.Values{authorizationDetailsParam: {""}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			claims := validIDJAGClaims()
			if tt.details != "" {
				claims = withDetails(t, claims, tt.details)
			}
			if tt.mutate != nil {
				tt.mutate(claims)
			}
			registry := tt.registry
			if tt.hook != nil {
				registry = newCallDetailsRegistry(t, tt.hook)
			}

			req, resp, consumer, err := redeemIDJAG(t, claims, registry, tt.form)

			if tt.wantCode != "" {
				status := tt.wantStatus
				if status == 0 {
					status = http.StatusBadRequest
				}
				requireRefusalStatus(t, err, status, tt.wantCode, tt.wantDesc)
				assert.Empty(t, consumer.key, "a refused ID-JAG must not be consumed")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "id-jag-jti", consumer.key)
			issued, ok := req.GetSession().(*session.Session)
			require.True(t, ok)
			granted, present := issued.JWTClaims.Extra[authorizationDetailsParam]
			echoed, echoPresent := resp.GetExtra(authorizationDetailsParam).(json.RawMessage)
			if tt.wantDetails == "" {
				assert.False(t, present, "a token without details must not carry the claim")
				assert.False(t, echoPresent, "the response must not carry the member")
				return
			}
			require.True(t, present)
			require.True(t, echoPresent)
			grantedRaw, ok := granted.(json.RawMessage)
			require.True(t, ok, "the token claim must be the raw granted JSON")
			assert.Equal(t, tt.wantDetails, string(grantedRaw), "the token carries the signed bytes")
			assert.Equal(t, string(grantedRaw), string(echoed), "the response echoes what the token carries")
		})
	}
}

// TestJWTBearerRedemptionRefusesDetailsParameter pins the parameter refusal on
// the plain grant, before the assertion is consumed.
func TestJWTBearerRedemptionRefusesDetailsParameter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		values   []string
		wantCode string // empty when the redemption is not refused
		wantDesc string
	}{
		{name: "absent"},
		{name: "one empty value is absent", values: []string{""}},
		{name: "a value", values: []string{"[" + testCallEntry + "]"},
			wantCode: "invalid_authorization_details", wantDesc: "parameter_not_allowed"},
		{name: "repeated empty values", values: []string{"", ""},
			wantCode: "invalid_request", wantDesc: hintDetailsParamRepeated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			consumer := &recordingAssertionConsumer{}
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{claims: refusalClaims()}, refusalPolicies(t))
			deps.consumer = consumer
			form := map[string][]string{
				"assertion": {signAssertionWithType(t, newTestJWKS(t), nil)},
				"resource":  {refusalResource},
			}
			if tt.values != nil {
				form[authorizationDetailsParam] = tt.values
			}

			err := newTestJWTBearerHandler(t, deps).HandleTokenEndpointRequest(context.Background(), newJWTBearerRequest(form))

			if tt.wantCode == "" {
				require.NoError(t, err)
				return
			}
			requireRefusal(t, err, tt.wantCode, tt.wantDesc)
			assert.Empty(t, consumer.key, "a refused assertion must not be consumed")
		})
	}
}

// TestIDJAGRedemptionDetailsHookRequest pins what the hook sees on a
// namespaced redemption: the authenticated client, the granted resource, and
// the ID-JAG's subject with no resolved user and without the claim's lossy
// decoded copy.
func TestIDJAGRedemptionDetailsHookRequest(t *testing.T) {
	t.Parallel()

	var seen authorizationdetails.Request
	_, _, _, err := redeemIDJAG(t, detailsClaims(t, "["+bigEntry+"]"),
		newCallDetailsRegistry(t, singleEntryHook(&seen)), nil)
	require.NoError(t, err)

	assert.Equal(t, oauthproto.GrantTypeJWTBearer, seen.GrantType)
	assert.Equal(t, idJAGTestClientID, seen.ClientID)
	assert.Equal(t, []string{idJAGTestResource}, seen.GrantedAudience)
	require.Len(t, seen.Entries, 1)
	assert.Equal(t, bigEntry, string(seen.Entries[0]))
	assert.Equal(t, idJAGTestSubject, seen.Subject.Subject)
	assert.Nil(t, seen.Exchange, "ID-JAG redemption leaves the exchange part nil")
	require.NotNil(t, seen.Redemption, "ID-JAG redemption sets the redemption part")
	assert.Empty(t, seen.Redemption.ResolvedUser, "a namespaced redemption resolves no user")
	assert.True(t, seen.Subject.External)
	assert.Equal(t, idJAGTestClientID, seen.Subject.ClientID)
	assert.NotContains(t, seen.Subject.Claims, authorizationDetailsParam)
	assert.Contains(t, seen.Subject.Claims, "act", "the other claims reach the hook")
}

// TestIDJAGRedemptionDetailsResolvedSubject runs the claim processing on a
// resolve issuer, which the subject-mode gate keeps closed in the full handler.
// The hook sees the resolved user next to the ID-JAG's own subject, and a
// subject that does not resolve is refused before any hook runs.
func TestIDJAGRedemptionDetailsResolvedSubject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		resolveErr  error
		wantRefusal bool
	}{
		{name: "resolved"},
		{name: "resolution fails before the hook", resolveErr: subject.ErrUnknown, wantRefusal: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := mocks.NewMockResolver(gomock.NewController(t))
			user := resolveTestUser
			if tt.resolveErr != nil {
				user = ""
			}
			resolver.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return(user, tt.resolveErr).Times(1)
			f := newResolveFixture(t, resolver)
			hookCalls := 0
			var seen authorizationdetails.Request
			record := singleEntryHook(&seen)
			f.h.detailsRegistry = newCallDetailsRegistry(t,
				func(ctx context.Context, req authorizationdetails.Request) (authorizationdetails.Result, error) {
					hookCalls++
					return record(ctx, req)
				})
			claims := withDetails(t, resolveClaims(validUpstreamSubject()), "["+testCallEntry+"]")
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			req.Client = f.client

			err := f.h.completeRedemption(context.Background(), req, claims, f.policy, idJAGTestResource)

			if tt.wantRefusal {
				require.Error(t, err)
				assert.Zero(t, hookCalls, "the hook must not run when the subject does not resolve")
				assert.Empty(t, f.consumer.key)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, hookCalls)
			assert.Nil(t, seen.Exchange)
			require.NotNil(t, seen.Redemption)
			assert.Equal(t, resolveTestUser, seen.Redemption.ResolvedUser)
			assert.Equal(t, idJAGTestSubject, seen.Subject.Subject, "Subject stays the ID-JAG's sub")
			issued, ok := req.GetSession().(*session.Session)
			require.True(t, ok)
			assert.Equal(t, resolveTestUser, issued.GetSubject())
			granted, ok := issued.JWTClaims.Extra[authorizationDetailsParam].(json.RawMessage)
			require.True(t, ok)
			assert.JSONEq(t, "["+testCallEntry+"]", string(granted))
		})
	}
}

// TestIDJAGRedemptionDetailsLogs pins the WARN line of each refusal kind: the
// client, a fixed reason, and for an unknown type the ID-JAG issuer and the
// entry types, never an entry's value.
//
//nolint:paralleltest // captures the process-global slog default
func TestIDJAGRedemptionDetailsLogs(t *testing.T) {
	logs := captureLogs(t)
	const secret = "secret-note-value"
	entry := `{"type":"` + testCallType + `","note":"` + secret + `"}`

	tests := []struct {
		name     string
		claims   *ValidatedClaims
		registry *authorizationdetails.Registry
		form     url.Values
		want     []string
	}{
		{name: "unknown type", claims: detailsClaims(t, "["+entry+"]"),
			want: []string{"reason=unknown_type", "issuer=" + idJAGTestIssuer, "types=[" + testCallType + "]"}},
		{name: "claim shape", claims: detailsClaims(t, `["`+secret+`"]`),
			want: []string{"reason=claim_not_array_of_objects"}},
		{name: "parameter", form: url.Values{authorizationDetailsParam: {"[" + entry + "]"}}, claims: validIDJAGClaims(),
			want: []string{"reason=parameter_not_allowed"}},
		{name: "parameter repeated", form: url.Values{authorizationDetailsParam: {"", ""}}, claims: validIDJAGClaims(),
			want: []string{"reason=repeated"}},
		{name: "hook output not signed", claims: detailsClaims(t, "["+entry+"]"),
			registry: newCallDetailsRegistry(t, grantingHook(testCallEntry)),
			want:     []string{"reason=hook_failed"}},
		{name: "empty grant", claims: detailsClaims(t, "["+entry+"]"),
			registry: newCallDetailsRegistry(t, grantingHook()),
			want:     []string{"reason=empty_grant", "types=[" + testCallType + "]"}},
		{name: "hook refusal", claims: detailsClaims(t, "["+entry+","+entry+"]"),
			registry: newCallDetailsRegistry(t, singleEntryHook(nil)),
			want:     []string{"reason=hook_rejected", "hook_reason=single_entry_required"}},
	}
	for _, tt := range tests {
		before := logs.String()
		_, _, _, err := redeemIDJAG(t, tt.claims, tt.registry, tt.form)
		require.Error(t, err, tt.name)
		added := strings.TrimPrefix(logs.String(), before)
		assert.Equal(t, 1, strings.Count(added, `msg="authorization_details rejected"`), tt.name)
		assert.Contains(t, added, "level=WARN", tt.name)
		assert.Contains(t, added, "client_id="+idJAGTestClientID, tt.name)
		for _, want := range tt.want {
			assert.Contains(t, added, want, tt.name)
		}
		assert.NotContains(t, added, secret, tt.name)
	}
}

// TestIDJAGDetailsRefusalLogLevel pins that an ID-JAG redemption's
// authorization_details refusal is logged at WARN only the first time: per
// (client, reason) for the parameter, which is refused before the assertion
// is validated, and per (issuer, reason) for the claim, whose refused ID-JAG
// is not consumed and can be resent.
//
//nolint:paralleltest // captures the process-global slog default
func TestIDJAGDetailsRefusalLogLevel(t *testing.T) {
	logs := captureLogs(t)

	tests := []struct {
		name       string
		claims     *ValidatedClaims
		form       url.Values
		wantReason string
	}{
		{name: "parameter", claims: validIDJAGClaims(),
			form: url.Values{authorizationDetailsParam: {"[" + testCallEntry + "]"}}, wantReason: "parameter_not_allowed"},
		{name: "parameter repeated", claims: validIDJAGClaims(),
			form: url.Values{authorizationDetailsParam: {"", ""}}, wantReason: "repeated"},
		{name: "claim", claims: detailsClaims(t, "["+testCallEntry+"]"), wantReason: "unknown_type"},
	}
	for _, tt := range tests {
		h := newIDJAGTestHandler(t, &testJWTBearerAssertionValidator{claims: tt.claims}, &recordingAssertionConsumer{})
		for _, wantLevel := range []string{"level=WARN", "level=DEBUG"} {
			req := newIDJAGRequest(t, newTestJWKS(t), idJAGTestClientID)
			maps.Copy(req.Form, tt.form)
			before := logs.String()
			require.Error(t, h.HandleTokenEndpointRequest(context.Background(), req), tt.name)
			added := strings.TrimPrefix(logs.String(), before)
			require.Equal(t, 1, strings.Count(added, `msg="authorization_details rejected"`), tt.name)
			assert.Contains(t, added, wantLevel, tt.name)
			assert.Contains(t, added, "reason="+tt.wantReason, tt.name)
		}
	}
}

// TestJWTBearerDetailsParameterLogLevel pins that the plain grant's parameter
// refusal, which a caller can repeat without client authentication, is logged
// at WARN only the first time per issuer. A redemption without the parameter
// does not count as that first time.
//
//nolint:paralleltest // captures the process-global slog default
func TestJWTBearerDetailsParameterLogLevel(t *testing.T) {
	logs := captureLogs(t)
	handler := newTestJWTBearerHandler(t,
		testIssuanceDeps(&testJWTBearerAssertionValidator{claims: refusalClaims()}, refusalPolicies(t)))
	assertion := signAssertionWithType(t, newTestJWKS(t), nil)
	require.NoError(t, handler.HandleTokenEndpointRequest(context.Background(), newJWTBearerRequest(
		map[string][]string{"assertion": {assertion}, "resource": {refusalResource}})))
	form := map[string][]string{
		"assertion":               {assertion},
		"resource":                {refusalResource},
		authorizationDetailsParam: {"[" + testCallEntry + "]"},
	}

	for _, wantLevel := range []string{"level=WARN", "level=DEBUG"} {
		before := logs.String()
		err := handler.HandleTokenEndpointRequest(context.Background(), newJWTBearerRequest(form))
		requireRefusal(t, err, "invalid_authorization_details", "parameter_not_allowed")
		added := strings.TrimPrefix(logs.String(), before)
		require.Equal(t, 1, strings.Count(added, `msg="authorization_details rejected"`))
		assert.Contains(t, added, wantLevel)
		assert.Contains(t, added, "reason=parameter_not_allowed")
		assert.Contains(t, added, "client_id="+jwtBearerClientID(refusalClaims().Issuer, refusalClaims().Subject))
	}
}

func TestEntryTypes(t *testing.T) {
	t.Parallel()

	entries := []json.RawMessage{
		json.RawMessage(`{"type":"` + testCallType + `"}`),
		json.RawMessage(`{"type":"` + testCallType + `"}`),
		json.RawMessage(`{"TYPE":"y"}`),
		json.RawMessage(`{"type":"has space"}`),
		json.RawMessage(`{"type":"` + strings.Repeat("t", 129) + `"}`),
	}
	for i := range maxLoggedTypes + 2 {
		entries = append(entries, json.RawMessage(`{"type":"t`+string(rune('a'+i))+`"}`))
	}

	types := entryTypes(entries)

	require.Len(t, types, maxLoggedTypes)
	assert.Equal(t, testCallType, types[0], "duplicates are listed once and another letter case is not the type")
	assert.Equal(t, invalidTypeLabel, types[1], "free-form and overlong types are not logged as written")
	assert.Equal(t, "ta", types[2])
}
