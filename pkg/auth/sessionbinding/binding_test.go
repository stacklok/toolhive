// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package sessionbinding

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive-core/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server/keys"
	keysmocks "github.com/stacklok/toolhive/pkg/authserver/server/keys/mocks"
)

func identity(iss, sub, token string) *auth.Identity {
	return &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: map[string]any{"iss": iss, "sub": sub}}, Token: token}
}

func TestBinding(t *testing.T) {
	t.Parallel()
	owner, err := FromIdentity(identity("issuer", "subject", "old"))
	require.NoError(t, err)
	for _, tc := range []struct {
		name, stored string
		caller       *auth.Identity
		valid        bool
	}{
		{"refresh", owner, identity("issuer", "subject", "new"), true},
		{"foreign subject", owner, identity("issuer", "foreign", "token"), false},
		{"foreign issuer", owner, identity("other", "subject", "token"), false},
		{"missing identity", owner, nil, false},
		{"legacy unowned", "", nil, false},
		{"corrupt owner", "issuer\x00sub\x00tail", identity("issuer", "sub", "token"), false},
		{"auth disabled", UnauthenticatedSentinel, nil, true},
		{"no downgrade", UnauthenticatedSentinel, &auth.Identity{}, false},
		{"no upgrade", UnauthenticatedSentinel, identity("issuer", "subject", "token"), false},
		{"synthetic identity", owner, identity("issuer", "subject", ""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.stored, tc.caller)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		})
	}
	for _, claims := range []map[string]any{
		nil, {"iss": "issuer"}, {"iss": 17, "sub": "subject"}, {"iss": "issuer", "sub": 17},
		{"iss": "", "sub": "subject"}, {"iss": "issuer", "sub": ""},
		{"iss": "issuer\x00tail", "sub": "subject"}, {"iss": "issuer", "sub": "sub\x00tail"},
	} {
		_, err := FromIdentity(&auth.Identity{PrincipalInfo: auth.PrincipalInfo{Claims: claims}})
		require.ErrorIs(t, err, ErrInvalidBinding)
	}
}

const (
	standardBinding = "private-issuer\x00private-subject"
	clientBinding   = standardBinding + "\x00delegated\x00private-client"
	actorBinding    = clientBinding + "\x00private-actor-issuer\x00private-actor-subject"
)

func delegatedIdentity(withActor bool) *auth.Identity {
	id := identity("private-issuer", "private-subject", "test-token")
	id.AdmissionRoute = auth.AdmissionRouteDelegated
	id.Claims["client_id"] = "private-client"
	if withActor {
		id.Claims["act"] = map[string]any{
			"iss": "private-actor-issuer", "sub": "private-actor-subject",
			"act": map[string]any{"iss": "private-prior-issuer", "sub": "private-prior-subject"},
		}
		id.DelegationChain = audit.ParseDelegationChain(id.Claims["act"], auth.DefaultMaxDelegationDepth)
	}
	return id
}

func TestFromIdentityRoutes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		change     func(*auth.Identity)
	}{
		{"no actor", clientBinding, func(id *auth.Identity) {
			delete(id.Claims, "act")
			id.DelegationChain = nil
		}},
		{"with actor", actorBinding, nil},
		{"truncated chain is not reparsed", actorBinding, func(id *auth.Identity) {
			id.DelegationChain.Truncated = true
			id.Claims["act"] = "malformed raw actor"
		}},
		{"nil chain fallback", actorBinding, func(id *auth.Identity) { id.DelegationChain = nil }},
		{"zero chain fallback", actorBinding, func(id *auth.Identity) { id.DelegationChain = &audit.DelegationChain{} }},
		{"interactive ignores malformed actor", standardBinding, func(id *auth.Identity) {
			id.AdmissionRoute = auth.AdmissionRouteInteractive
			id.DelegationChain.Malformed = true
		}},
		{"unspecified with client and actor", standardBinding, func(id *auth.Identity) {
			id.AdmissionRoute = auth.AdmissionRouteUnspecified
		}},
		{"interactive ignores null actor", standardBinding, func(id *auth.Identity) {
			id.AdmissionRoute = auth.AdmissionRouteInteractive
			id.Claims["act"] = nil
			id.DelegationChain = nil
		}},
		{"sessionless ignores malformed actor", standardBinding, func(id *auth.Identity) {
			id.AdmissionRoute = auth.AdmissionRouteUnspecified
			id.SessionlessGrant = true
			id.DelegationChain.Malformed = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := delegatedIdentity(true)
			if tc.change != nil {
				tc.change(id)
			}
			stored, err := FromIdentity(id)
			require.NoError(t, err)
			require.Equal(t, tc.want, stored)
			iss, sub, ok := Parse(stored)
			require.True(t, ok)
			require.Equal(t, "private-issuer", iss)
			require.Equal(t, "private-subject", sub)
			require.NoError(t, Validate(stored, id))
		})
	}
}

func TestFromIdentityRejectsInvalidBinding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*auth.Identity)
	}{
		{"missing client", func(id *auth.Identity) { delete(id.Claims, "client_id") }},
		{"non-string client", func(id *auth.Identity) { id.Claims["client_id"] = 91234 }},
		{"empty client", func(id *auth.Identity) { id.Claims["client_id"] = "" }},
		{"NUL in client", func(id *auth.Identity) { id.Claims["client_id"] = "private-client\x00tail" }},
		{"malformed outer actor", func(id *auth.Identity) {
			id.Claims["act"] = "private-malformed-actor"
			id.DelegationChain = audit.ParseDelegationChain(id.Claims["act"], auth.DefaultMaxDelegationDepth)
		}},
		{"malformed nested actor", func(id *auth.Identity) {
			id.Claims["act"].(map[string]any)["act"] = "private-malformed-actor"
			id.DelegationChain = audit.ParseDelegationChain(id.Claims["act"], auth.DefaultMaxDelegationDepth)
		}},
		{"nonzero chain without actor", func(id *auth.Identity) {
			id.DelegationChain = &audit.DelegationChain{Truncated: true}
		}},
		{"empty actor issuer", func(id *auth.Identity) { id.DelegationChain.Chain[0].Issuer = "" }},
		{"empty actor subject", func(id *auth.Identity) { id.DelegationChain.Chain[0].Subject = "" }},
		{"explicit null act", func(id *auth.Identity) {
			id.Claims["act"] = nil
			id.DelegationChain = nil
		}},
		{"empty act object", func(id *auth.Identity) {
			id.Claims["act"] = map[string]any{}
			id.DelegationChain = nil
		}},
		{"NUL in issuer", func(id *auth.Identity) { id.Claims["iss"] = "private-issuer\x00tail" }},
		{"NUL in subject", func(id *auth.Identity) { id.Claims["sub"] = "private-subject\x00tail" }},
		{"NUL in actor issuer", func(id *auth.Identity) { id.DelegationChain.Chain[0].Issuer += "\x00tail" }},
		{"NUL in actor subject", func(id *auth.Identity) { id.DelegationChain.Chain[0].Subject += "\x00tail" }},
		{"unknown route", func(id *auth.Identity) { id.AdmissionRoute = auth.AdmissionRoute("future") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id := delegatedIdentity(true)
			tc.change(id)
			stored, err := FromIdentity(id)
			require.ErrorIs(t, err, ErrInvalidBinding)
			require.Empty(t, stored)
			for _, value := range []string{"private-", "91234", "test-token", "future", "tail"} {
				require.NotContains(t, err.Error(), value)
			}
		})
	}
}

func TestValidateRejectsUnbindableIdentity(t *testing.T) {
	t.Parallel()
	id := delegatedIdentity(true)
	id.Claims["client_id"] = ""
	require.ErrorIs(t, Validate(actorBinding, id), ErrNotFound)
	require.ErrorIs(t, Validate(UnauthenticatedSentinel, id), ErrNotFound)
}

func TestValidateDelegatedBinding(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stored string
		withActor    bool
		change       func(*auth.Identity)
		valid        bool
	}{
		{"different client", actorBinding, true, func(id *auth.Identity) { id.Claims["client_id"] = "other" }, false},
		{"different actor issuer", actorBinding, true, func(id *auth.Identity) {
			id.DelegationChain.Chain[0].Issuer = "other"
		}, false},
		{"different actor subject", actorBinding, true, func(id *auth.Identity) {
			id.DelegationChain.Chain[0].Subject = "other"
		}, false},
		{"actor cannot reuse actorless binding", clientBinding, true, nil, false},
		{"actorless cannot reuse actor binding", actorBinding, false, nil, false},
		{"explicit null act cannot reuse actorless binding", clientBinding, false, func(id *auth.Identity) {
			id.Claims["act"] = nil
		}, false},
		{"interactive cannot reuse delegated binding", actorBinding, true, func(id *auth.Identity) {
			id.AdmissionRoute = auth.AdmissionRouteInteractive
		}, false},
		{"delegated cannot reuse interactive binding", standardBinding, true, nil, false},
		{"renewed token", actorBinding, true, func(id *auth.Identity) {
			id.Claims["jti"] = "new-id"
			id.Claims["exp"] = 1234567890
			id.Claims["authorization_details"] = []any{map[string]any{"type": "new-type"}}
			id.DelegationChain.Chain[1].Issuer = "other-nested-issuer"
			id.DelegationChain.Chain[1].Subject = "other-nested-subject"
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			caller := delegatedIdentity(tc.withActor)
			if tc.change != nil {
				tc.change(caller)
			}
			err := Validate(tc.stored, caller)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, stored string
		valid        bool
	}{
		{"standard", "iss\x00sub", true},
		{"delegated without actor", "iss\x00sub\x00delegated\x00client", true},
		{"delegated with actor", "iss\x00sub\x00delegated\x00client\x00act-iss\x00act-sub", true},
		{"empty", "", false},
		{"sentinel", UnauthenticatedSentinel, false},
		{"one field", "iss", false},
		{"three fields", "iss\x00sub\x00trailer", false},
		{"missing client field", "iss\x00sub\x00delegated", false},
		{"five fields", "iss\x00sub\x00delegated\x00client\x00act-iss", false},
		{"seven fields", "iss\x00sub\x00delegated\x00client\x00act-iss\x00act-sub\x00extra", false},
		{"unknown route", "iss\x00sub\x00interactive\x00client", false},
		{"empty route", "iss\x00sub\x00\x00client", false},
		{"empty issuer", "\x00sub\x00delegated\x00client", false},
		{"empty subject", "iss\x00\x00delegated\x00client", false},
		{"empty client", "iss\x00sub\x00delegated\x00", false},
		{"empty actor issuer", "iss\x00sub\x00delegated\x00client\x00\x00act-sub", false},
		{"empty actor subject", "iss\x00sub\x00delegated\x00client\x00act-iss\x00", false},
		{"both actor components empty", "iss\x00sub\x00delegated\x00client\x00\x00", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			iss, sub, ok := Parse(tc.stored)
			require.Equal(t, tc.valid, ok)
			if tc.valid {
				require.Equal(t, "iss", iss)
				require.Equal(t, "sub", sub)
			} else {
				require.Empty(t, iss)
				require.Empty(t, sub)
			}
		})
	}
}

func TestFormatBindingRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		fields parsedBinding
	}{
		{"unexpected delegated fields", parsedBinding{clientID: "private-client"}},
		{"missing actor subject", parsedBinding{
			route: string(auth.AdmissionRouteDelegated), clientID: "private-client", actIss: "private-actor-issuer",
		}},
		{"missing actor issuer", parsedBinding{
			route: string(auth.AdmissionRouteDelegated), clientID: "private-client", actSub: "private-actor-subject",
		}},
		{"unsupported route", parsedBinding{route: "private-route"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := tc.fields
			b.iss, b.sub = "private-issuer", "private-subject"
			formatted, err := formatBinding(b)
			require.ErrorIs(t, err, ErrInvalidBinding)
			require.Empty(t, formatted)
			for _, field := range []string{b.iss, b.sub, b.route, b.clientID, b.actIss, b.actSub} {
				if field != "" {
					require.NotContains(t, err.Error(), field)
				}
			}
		})
	}
}

func TestBindingFromTokenValidator(t *testing.T) {
	t.Parallel()
	const kid, issuer, audience = "test-key", "https://issuer.example.com", "test-resource"
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	sign := func(t *testing.T, clientID, actorSubject string) string {
		t.Helper()
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": issuer, "sub": "user", "aud": audience, "exp": time.Now().Add(time.Hour).Unix(),
			claimnames.DelegatedAccessKey: true,
			"client_id":                   clientID,
			"act":                         map[string]any{"iss": "actor-issuer", "sub": actorSubject},
		})
		token.Header["kid"] = kid
		signed, err := token.SignedString(key)
		require.NoError(t, err)
		return signed
	}
	token := sign(t, "client", "actor")
	for _, tc := range []struct {
		name, want string
		route      auth.AdmissionRoute
		bound      bool
	}{
		{"bound validator", issuer + "\x00user\x00delegated\x00client\x00actor-issuer\x00actor", auth.AdmissionRouteDelegated, true},
		{"unbound validator", issuer + "\x00user", auth.AdmissionRouteUnspecified, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := keysmocks.NewMockPublicKeyProvider(gomock.NewController(t))
			provider.EXPECT().PublicKeys(gomock.Any()).Return([]*keys.PublicKeyData{
				{KeyID: kid, Algorithm: "RS256", PublicKey: &key.PublicKey},
			}, nil).AnyTimes()
			opts := []auth.TokenValidatorOption{auth.WithKeyProvider(provider)}
			if tc.bound {
				opts = append(opts, auth.WithEmbeddedAuthServerIssuer(issuer))
			}
			// An explicit fallback URL prevents discovery; the mock supplies the signing key.
			validator, err := auth.NewTokenValidator(t.Context(), auth.TokenValidatorConfig{
				Issuer: issuer, Audience: audience, JWKSURL: issuer + "/jwks",
			}, opts...)
			require.NoError(t, err)

			serve := func(signed string) *auth.Identity {
				t.Helper()
				var id *auth.Identity
				handler := validator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					id, _ = auth.IdentityFromContext(r.Context())
					w.WriteHeader(http.StatusNoContent)
				}))
				req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
				req.Header.Set("Authorization", "Bearer "+signed)
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
				require.NotNil(t, id)
				require.Equal(t, tc.route, id.AdmissionRoute)
				return id
			}
			id := serve(token)
			stored, err := FromIdentity(id)
			require.NoError(t, err)
			require.Equal(t, tc.want, stored)
			require.NoError(t, Validate(stored, id))
			if tc.bound {
				require.ErrorIs(t, Validate(stored, serve(sign(t, "other-client", "actor"))), ErrNotFound)
				require.ErrorIs(t, Validate(stored, serve(sign(t, "client", "other-actor"))), ErrNotFound)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	t.Parallel()
	storageErr := errors.New("private storage detail")
	for _, tc := range []struct {
		name, sid, owner string
		lookupErr        error
		caller           *auth.Identity
		status           int
	}{
		{name: "owner", sid: "id", owner: "issuer\x00subject", caller: identity("issuer", "subject", "fresh"), status: 204},
		{name: "foreign", sid: "id", owner: "issuer\x00subject", caller: identity("other", "subject", "token"), status: 404},
		{name: "unowned", sid: "id", status: 404},
		{name: "unknown", sid: "id", lookupErr: ErrNotFound, status: 404},
		{name: "storage failure", sid: "id", lookupErr: storageErr, status: 503},
		{name: "sessionless", status: 204},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called, lookedUp := false, false
			middleware, err := NewMiddleware(func(*http.Request) (string, error) { return tc.sid, nil },
				func(context.Context, string) (string, error) { lookedUp = true; return tc.owner, tc.lookupErr },
				func(w http.ResponseWriter, _ *http.Request, err error) { WriteOwnershipError(w, nil, err) })
			require.NoError(t, err)
			next := middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; w.WriteHeader(204) }))
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			req = req.WithContext(auth.WithIdentity(req.Context(), tc.caller))
			rec := httptest.NewRecorder()
			next.ServeHTTP(rec, req)
			require.Equal(t, tc.status, rec.Code)
			require.Equal(t, tc.status == 204, called)
			require.Equal(t, tc.sid != "", lookedUp)
			require.NotContains(t, rec.Body.String(), "private")
			require.NotContains(t, rec.Body.String(), "subject")
		})
	}
	_, err := NewMiddleware(nil, nil, nil)
	require.Error(t, err)
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query string
		headers     []string
		valid       bool
	}{
		{"query", "?sessionId=opaque%2Fid", nil, true},
		{"matching", "?sessionId=id", []string{"id"}, true},
		{"conflicting", "?sessionId=other", []string{"id"}, false},
		{"duplicate query", "?sessionId=id&sessionId=other", nil, false},
		{"duplicate header", "", []string{"id", "other"}, false},
		{"malformed query", "?sessionId=id%ZZ", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/messages"+tc.query, nil)
			req.Header[http.CanonicalHeaderKey("Mcp-Session-Id")] = tc.headers
			_, err := RequestID(req, "sessionId")
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, ErrNotFound)
			}
		})
	}
}
