// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const (
	grantMintTarget   = "https://as2.example.com"
	grantMintResource = "https://gw.example.com/mcp"
	grantMintClientAt = "broker-at-as2"
	// grantMintUpstream and grantMintSubID are what a grant records at login.
	grantMintUpstream = `{"iss":"https://idp.example","claim":"oid","value":"unit-upstream-value"}`
	grantMintSubID    = `{"format":"iss_sub","iss":"https://idp.example","sub":"unit-sub-id"}`
)

// idJAGHandler is deriveHandler with one ID-JAG target for the broker that
// requires the given subject claims, a real signer and an issuer.
func idJAGHandler(t *testing.T, store storage.ExchangeGrantStorage, required ...string) *RefreshTokenHandler {
	t.Helper()
	h := deriveHandler(t, store)
	policy := h.clientPolicies[testRefreshClient]
	policy.IDJAGTargets = map[string]IDJAGTarget{grantMintTarget: {
		Issuer: grantMintTarget, ClientID: grantMintClientAt, Resources: []string{grantMintResource},
		Lifetime: time.Minute, RequiredSubjectClaims: required,
	}}
	h.clientPolicies[testRefreshClient] = policy
	h.signer = newMintTestSigner(newTestJWKS(t))
	h.issuer = testIssuer
	return h
}

// idJAGRequest asks for an ID-JAG for the test target, presenting token.
func idJAGRequest(token string) *fosite.AccessRequest {
	req := refreshRequestForTest(brokerClientForTest())
	req.Form = url.Values{
		"grant_type":           {oauthproto.GrantTypeTokenExchange},
		"subject_token":        {token},
		"subject_token_type":   {oauthproto.TokenTypeRefreshToken},
		"requested_token_type": {oauthproto.TokenTypeIDJAG},
		"audience":             {grantMintTarget},
		"resource":             {grantMintResource},
	}
	return req
}

func idJAGRecord(sig, client, parent string, expires time.Time, types ...string) storage.ExchangeGrant {
	return storage.ExchangeGrant{
		Signature: sig, ClientID: client, ParentSignature: parent, Subject: "user-1",
		UpstreamSubject: grantMintUpstream, SubjectIdentifier: grantMintSubID,
		IssuableTokenTypes: types, ExpiresAt: expires, CreatedAt: time.Now(),
	}
}

func idJAGRecords(records ...storage.ExchangeGrant) map[string]storage.ExchangeGrant {
	out := make(map[string]storage.ExchangeGrant, len(records))
	for _, r := range records {
		out[r.Signature] = r
	}
	return out
}

// idJAGStore serves the given grants and records any grant written.
func idJAGStore(records ...storage.ExchangeGrant) *fakeGrantStoreWithRecords {
	return &fakeGrantStoreWithRecords{recordStore: recordStore{grants: idJAGRecords(records...)}}
}

func TestIDJAGCheckOrder(t *testing.T) {
	t.Parallel()

	live := time.Now().Add(time.Hour)
	idJAG := oauthproto.TokenTypeIDJAG
	refresh := oauthproto.TokenTypeRefreshToken
	revoked := idJAGRecord("revoked", testRefreshClient, "", live, idJAG)
	revoked.RevokedAt = time.Now()
	// A grant with under one whole second left is expired: the check compares
	// whole seconds, so ExpiresAt in the current second is already past.
	subSecond := time.Now().Truncate(time.Second).Add(500 * time.Millisecond)
	records := []storage.ExchangeGrant{
		idJAGRecord("ok", testRefreshClient, "", live, idJAG),
		idJAGRecord("stranger", "stranger", "", live, idJAG),
		idJAGRecord("expired", testRefreshClient, "", time.Now().Add(-time.Hour), idJAG),
		idJAGRecord("subsecond", testRefreshClient, "", subSecond, idJAG),
		idJAGRecord("parent", testRefreshClient, "", live, refresh),
		idJAGRecord("child", testRefreshClient, "parent", live, idJAG),
		idJAGRecord("refresh-only", testRefreshClient, "", live, refresh),
		revoked,
	}

	tests := []struct {
		name  string
		token string
		want  string // error_description; "" mints
	}{
		{name: "a live top-level grant mints", token: "x.ok"},
		{name: "a live child grant mints", token: "x.child"},
		{name: "another client's grant", token: "x.stranger", want: reasonGrantInvalid},
		{name: "an unknown signature", token: "x.nothere", want: reasonGrantInvalid},
		{name: "an expired grant", token: "x.expired", want: reasonGrantInvalid},
		{name: "a grant with under one whole second left", token: "x.subsecond", want: reasonGrantInvalid},
		{name: "a revoked grant", token: "x.revoked", want: reasonRevoked},
		{name: "a grant that does not list the ID-JAG output", token: "x.refresh-only", want: reasonNotIssuable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := idJAGStore(records...)
			h := idJAGHandler(t, store)
			req := idJAGRequest(tt.token)
			resp := fosite.NewAccessResponse()
			handleErr := h.HandleTokenEndpointRequest(t.Context(), req)
			populateErr := h.PopulateTokenEndpointResponse(t.Context(), req, resp)
			assert.Empty(t, store.created, "minting and refusing write nothing")
			if tt.want == "" {
				require.NoError(t, handleErr)
				require.NoError(t, populateErr)
				assert.NotEmpty(t, resp.GetAccessToken())
				return
			}
			for _, err := range []error{handleErr, populateErr} {
				requireOAuthError(t, err, "invalid_request", tt.want)
			}
			assert.Empty(t, resp.GetAccessToken())
		})
	}

	t.Run("request parameters are checked before the store is read", func(t *testing.T) {
		t.Parallel()
		store := &recordStore{grants: idJAGRecords(records...)}
		h := idJAGHandler(t, store)
		req := idJAGRequest("x.ok")
		req.Form.Del("resource")
		requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req), "invalid_target", "")
		req = idJAGRequest("bad.sig")
		requireOAuthError(t, h.HandleTokenEndpointRequest(t.Context(), req), "invalid_request", reasonGrantInvalid)
		assert.Zero(t, store.reads, "neither a request refusal nor a failed HMAC reads the store")
	})
}

func TestIDJAGMintsFromTheGrantSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		required []string
		want     map[string]string // signed subject claims; any other is absent
	}{
		{name: "both claims required", required: []string{"upstream_subject", "sub_id"},
			want: map[string]string{claimnames.UpstreamSubjectKey: grantMintUpstream, "sub_id": grantMintSubID}},
		{name: "a claim the target does not require is not signed", required: []string{"upstream_subject"},
			want: map[string]string{claimnames.UpstreamSubjectKey: grantMintUpstream}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := idJAGStore(idJAGRecord("ok", testRefreshClient, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG))
			h := idJAGHandler(t, store, tt.required...)
			req := idJAGRequest("x.ok")
			// The subject comes from the grant, never from the request.
			req.Form.Set("sub", "attacker")
			req.Form.Set("sub_id", `{"format":"opaque","id":"attacker"}`)
			jtis := map[string]bool{}
			for range 2 {
				resp := fosite.NewAccessResponse()
				require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))
				require.NoError(t, h.PopulateTokenEndpointResponse(t.Context(), req, resp))

				assert.Empty(t, req.GetGrantedAudience(), "the ID-JAG path never grants an audience")
				claims := decodeJWTPart(t, resp.GetAccessToken(), 1)
				assert.Equal(t, "user-1", claims["sub"])
				for _, key := range []string{claimnames.UpstreamSubjectKey, "sub_id"} {
					want, ok := tt.want[key]
					if !ok {
						assert.NotContains(t, claims, key)
						continue
					}
					got, err := json.Marshal(claims[key])
					require.NoError(t, err)
					assert.JSONEq(t, want, string(got), key)
				}
				jti, _ := claims["jti"].(string)
				jtis[jti] = true
			}
			assert.Len(t, jtis, 2, "each mint has its own jti")
			assert.Empty(t, store.created, "minting writes nothing")
		})
	}
}

// TestIDJAGInternalFailuresAreGeneric covers internal failures on the ID-JAG
// path that the derive tests do not: each is a server_error whose description
// carries no cause.
func TestIDJAGInternalFailuresAreGeneric(t *testing.T) {
	t.Parallel()

	good := idJAGRecord("ok", testRefreshClient, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG)
	badSubID := good
	badSubID.SubjectIdentifier = "{not json"
	tests := []struct {
		name  string
		grant storage.ExchangeGrant
		setup func(*RefreshTokenHandler)
		leaks []string
	}{
		{name: "signing failure", grant: good, setup: func(h *RefreshTokenHandler) {
			h.signer = &recordingSigner{err: errors.New("signing key kid-42 is missing")}
		}, leaks: []string{"kid-42"}},
		{name: "stored sub_id is not JSON although not required", grant: badSubID, leaks: []string{"not json", "sub_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := idJAGHandler(t, idJAGStore(tt.grant), "upstream_subject")
			if tt.setup != nil {
				tt.setup(h)
			}
			resp := fosite.NewAccessResponse()
			err := h.PopulateTokenEndpointResponse(t.Context(), idJAGRequest("x.ok"), resp)
			oauthErr := unwrapOAuth(t, err)
			assert.Equal(t, "server_error", oauthErr.ErrorField)
			for _, leak := range tt.leaks {
				assert.NotContains(t, oauthErr.GetDescription(), leak)
			}
			assert.Empty(t, resp.GetAccessToken())
		})
	}
}

func TestIDJAGDecidesAgainBeforeMinting(t *testing.T) {
	t.Parallel()

	store := idJAGStore(idJAGRecord("ok", testRefreshClient, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG))
	h := idJAGHandler(t, store)
	req := idJAGRequest("x.ok")
	require.NoError(t, h.HandleTokenEndpointRequest(t.Context(), req))

	revoked := store.grants["ok"]
	revoked.RevokedAt = time.Now()
	store.grants["ok"] = revoked
	resp := fosite.NewAccessResponse()
	requireOAuthError(t, h.PopulateTokenEndpointResponse(t.Context(), req, resp), "invalid_request", reasonRevoked)
	assert.Empty(t, resp.GetAccessToken(), "nothing is minted")
}

// TestIDJAGMintUsesGrantExpiryAndDecisionTime sets the clock by value: decide
// takes now, so no wall clock is read.
func TestIDJAGMintUsesGrantExpiryAndDecisionTime(t *testing.T) {
	t.Parallel()

	// exchangeGrantNow is a whole second; the offsets below rely on it.
	base := exchangeGrantNow
	record := idJAGRecord("ok", testRefreshClient, "", base.Add(1500*time.Millisecond), oauthproto.TokenTypeIDJAG)
	record.CreatedAt = base
	h := idJAGHandler(t, &recordStore{grants: idJAGRecords(record)})
	decided, err := h.decide(t.Context(), idJAGRequest("x.ok"), base.Add(200*time.Millisecond))
	require.NoError(t, err)
	require.NotNil(t, decided.idJAG)
	resp := fosite.NewAccessResponse()
	require.NoError(t, h.issueIDJAG(t.Context(), decided.idJAG, resp))
	claims := decodeJWTPart(t, resp.GetAccessToken(), 1)
	assert.Equal(t, json.Number("1800000001"), claims["exp"], "exp is the floor of the grant's expiry")
	assert.Equal(t, json.Number("1800000000"), claims["iat"], "iat is the decision time")
}

// TestIDJAGMintIsBoundedByTheCurrentCap sets the clock by value. The client's
// current id_jag_max_lifetime bounds a grant issued under a larger one.
func TestIDJAGMintIsBoundedByTheCurrentCap(t *testing.T) {
	t.Parallel()

	base := exchangeGrantNow
	tests := []struct {
		name    string
		created time.Time
		expires time.Time
		cap     time.Duration
		wantExp json.Number // "" is refused as an invalid grant
		corrupt bool        // refused with server_error
	}{
		{name: "a cap lowered below the remaining life bounds exp",
			created: base.Add(-100 * time.Second), expires: base.Add(time.Hour), cap: 130 * time.Second,
			wantExp: "1800000030"},
		{name: "a cap that does not bind leaves the target lifetime",
			created: base.Add(-100 * time.Second), expires: base.Add(time.Hour), cap: time.Hour,
			wantExp: "1800000060"},
		{name: "a grant already past the lowered cap is refused",
			created: base.Add(-2 * time.Hour), expires: base.Add(time.Hour), cap: time.Hour},
		{name: "a grant whose cap ends in this second is refused",
			created: base.Add(-time.Hour), expires: base.Add(time.Hour), cap: time.Hour},
		{name: "a zero creation time is a corrupt record and is refused",
			expires: base.Add(30 * time.Second), cap: time.Hour, corrupt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			record := idJAGRecord("ok", testRefreshClient, "", tt.expires, oauthproto.TokenTypeIDJAG)
			record.CreatedAt = tt.created
			store := idJAGStore(record)
			h := idJAGHandler(t, store)
			policy := h.clientPolicies[testRefreshClient]
			policy.ExchangeRefreshTokens = map[string]time.Duration{oauthproto.TokenTypeIDJAG: tt.cap}
			h.clientPolicies[testRefreshClient] = policy

			decided, err := h.decide(t.Context(), idJAGRequest("x.ok"), base)
			if tt.corrupt {
				requireOAuthError(t, err, "server_error", "")
				return
			}
			if tt.wantExp == "" {
				requireOAuthError(t, err, "invalid_request", reasonGrantInvalid)
				assert.Empty(t, store.created, "nothing is minted")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, decided.idJAG)
			resp := fosite.NewAccessResponse()
			require.NoError(t, h.issueIDJAG(t.Context(), decided.idJAG, resp))
			assert.Equal(t, tt.wantExp, decodeJWTPart(t, resp.GetAccessToken(), 1)["exp"])
		})
	}
}

// TestRefreshTokenSubjectRequestGating covers a refresh-token subject under
// each set of exchange_refresh_tokens entries and ID-JAG targets a client's
// current policy may hold. Each requested type needs its own entry; without it
// the request is refused with the subject_token_type text, so removing an entry
// stops derives and mints from grants the client already holds.
func TestRefreshTokenSubjectRequestGating(t *testing.T) {
	t.Parallel()

	const (
		subjectTxt = "'subject_token_type' value"
		requestTxt = "'requested_token_type' value"
		// deriveTxt is decideChild's refusal: the request carries the ID-JAG
		// audience and resource, which a refresh-token derive does not accept.
		deriveTxt = "parameters are not supported when requesting a refresh token"
		mint      = "mints"
	)
	both := map[string]time.Duration{oauthproto.TokenTypeRefreshToken: time.Hour, oauthproto.TokenTypeIDJAG: time.Hour}
	idJAGOnly := map[string]time.Duration{oauthproto.TokenTypeIDJAG: time.Hour}
	refreshOnly := map[string]time.Duration{oauthproto.TokenTypeRefreshToken: time.Hour}

	tests := []struct {
		name    string
		caps    map[string]time.Duration
		targets bool
		// want is the substring of the refusal for each requested type, or mint.
		refresh, idJAG, access string
	}{
		{name: "both entries with targets", caps: both, targets: true, refresh: deriveTxt, idJAG: mint, access: subjectTxt},
		{name: "ID-JAG entry only with targets", caps: idJAGOnly, targets: true, refresh: subjectTxt, idJAG: mint, access: subjectTxt},
		{name: "refresh_token entry only with targets", caps: refreshOnly, targets: true,
			refresh: deriveTxt, idJAG: subjectTxt, access: subjectTxt},
		{name: "ID-JAG entry without targets", caps: idJAGOnly, refresh: subjectTxt, idJAG: requestTxt, access: subjectTxt},
		{name: "no entries", targets: true, refresh: subjectTxt, idJAG: subjectTxt, access: subjectTxt},
	}
	for _, tt := range tests {
		for requested, want := range map[string]string{
			oauthproto.TokenTypeRefreshToken: tt.refresh,
			oauthproto.TokenTypeIDJAG:        tt.idJAG,
			oauthproto.TokenTypeAccessToken:  tt.access,
		} {
			t.Run(tt.name+" asking for "+requested, func(t *testing.T) {
				t.Parallel()
				store := idJAGStore(idJAGRecord("ok", testRefreshClient, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG))
				h := idJAGHandler(t, store)
				policy := h.clientPolicies[testRefreshClient]
				policy.ExchangeRefreshTokens = tt.caps
				if !tt.targets {
					policy.IDJAGTargets = nil
				}
				h.clientPolicies[testRefreshClient] = policy

				req := idJAGRequest("x.ok")
				req.Form.Set("requested_token_type", requested)
				resp := fosite.NewAccessResponse()
				handleErr := h.HandleTokenEndpointRequest(t.Context(), req)
				if want == mint {
					require.NoError(t, handleErr)
					require.NoError(t, h.PopulateTokenEndpointResponse(t.Context(), req, resp))
					assert.Equal(t, oauthproto.TokenTypeIDJAG, resp.GetExtra("issued_token_type"))
					assert.NotEmpty(t, resp.GetAccessToken())
					return
				}
				wantCode := "invalid_request"
				if want == deriveTxt {
					wantCode = "invalid_target"
				}
				requireOAuthError(t, handleErr, wantCode, "")
				assert.Contains(t, unwrapOAuth(t, handleErr).GetDescription(), want)
				assert.Empty(t, resp.GetAccessToken())
			})
		}
	}
}

// linesFor returns the lines of captured log output that name clientID.
func linesFor(logs, clientID string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "client_id="+clientID+" ") {
			out = append(out, line)
		}
	}
	return out
}

// TestIDJAGLogs pins what the ID-JAG path logs on failure: one WARN line with
// the reason and without sub_id, the presented token or upstream_subject. A
// signing failure keeps its cause off the line, in the error's debug field,
// which the token endpoint logs at ERROR (serverFailure). A successful mint is
// one Debug line with the jti and sub_id. Each case uses a client_id no other
// test uses, because the default logger is process-wide.
//
//nolint:paralleltest // captures the process-global slog default
func TestIDJAGLogs(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	prevWriter, prevFlags := log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		// slog.SetDefault also redirects the log package's output and flags,
		// and restoring the previous slog default does not undo that.
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	const token = "presented-token-value.ok"
	tests := []struct {
		name     string
		clientID string
		required []string
		mutate   func(*storage.ExchangeGrant)
		profiled bool // the client has an actor profile
		signer   *recordingSigner
		code     string // "" is a successful mint
		reason   string
		debug    string // cause carried in the error's debug field, not on the line
	}{
		{name: "a successful mint", clientID: "idjag-log-unit-ok"},
		{name: "a revoked grant", clientID: "idjag-log-unit-revoked",
			mutate: func(g *storage.ExchangeGrant) { g.RevokedAt = time.Now() },
			code:   "invalid_request", reason: reasonRevoked},
		{name: "a missing claim", clientID: "idjag-log-unit-missing", required: []string{"upstream_subject"},
			mutate: func(g *storage.ExchangeGrant) { g.UpstreamSubject = "" },
			code:   "invalid_request", reason: reasonUpstreamSubjectMissing},
		{name: "a client with an actor profile", clientID: "idjag-log-unit-profiled", profiled: true,
			code: "unauthorized_client", reason: refuseActorUnavailable},
		{name: "a signing failure", clientID: "idjag-log-unit-sign",
			signer: &recordingSigner{err: errors.New("signing key kid-42 is missing")},
			code:   "server_error", reason: refuseInternal, debug: "kid-42"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grant := idJAGRecord("ok", tt.clientID, "", time.Now().Add(time.Hour), oauthproto.TokenTypeIDJAG)
			if tt.mutate != nil {
				tt.mutate(&grant)
			}
			h := idJAGHandler(t, idJAGStore(grant), tt.required...)
			policy := h.clientPolicies[testRefreshClient]
			if tt.profiled {
				policy.ActorProfile = &ActorProfile{Name: "broker", ActorTokenType: oauthproto.TokenTypeJWT}
			}
			h.clientPolicies[tt.clientID] = policy
			if tt.signer != nil {
				h.signer = tt.signer
			}
			req := idJAGRequest(token)
			req.Client = &fosite.DefaultClient{ID: tt.clientID, GrantTypes: fosite.Arguments{oauthproto.GrantTypeTokenExchange}}

			resp := fosite.NewAccessResponse()
			err := h.HandleTokenEndpointRequest(t.Context(), req)
			if err == nil {
				err = h.PopulateTokenEndpointResponse(t.Context(), req, resp)
			}
			if tt.code == "" {
				require.NoError(t, err)
				lines := linesFor(logs.String(), tt.clientID)
				require.Len(t, lines, 1)
				assert.Contains(t, lines[0], "level=DEBUG")
				assert.Contains(t, lines[0], `msg="ID-JAG minted"`)
				jti, _ := decodeJWTPart(t, resp.GetAccessToken(), 1)["jti"].(string)
				for _, field := range []string{"jti=" + jti, "unit-sub-id", "target=", "resource=", "has_parent=false"} {
					assert.Contains(t, lines[0], field)
				}
				for _, secret := range []string{token, resp.GetAccessToken(), "unit-upstream-value"} {
					assert.NotContains(t, lines[0], secret)
				}
				return
			}
			requireOAuthError(t, err, tt.code, "")
			if tt.debug != "" {
				assert.Contains(t, unwrapOAuth(t, err).DebugField, tt.debug)
				assert.NotContains(t, unwrapOAuth(t, err).GetDescription(), tt.debug)
			}

			lines := linesFor(logs.String(), tt.clientID)
			require.Len(t, lines, 1)
			assert.Contains(t, lines[0], "level=WARN")
			assert.Contains(t, lines[0], "reason="+tt.reason)
			for _, secret := range []string{token, "unit-upstream-value", "unit-sub-id", "kid-42"} {
				assert.NotContains(t, lines[0], secret)
			}
		})
	}
}
