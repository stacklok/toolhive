// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	RegisterModifiers()
	os.Exit(m.Run())
}

func makeJWT(payload string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	b := base64.RawURLEncoding.EncodeToString([]byte(payload))
	return h + "." + b + ".sig"
}

func TestExtractIdentityFromTokenResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      []byte
		cfg       *IdentityFromTokenConfig
		want      partialIdentity
		wantErr   bool
		wantErrIs error
	}{
		{
			name: "snowflake flat happy path",
			body: []byte(`{"access_token":"opaque-blob","expires_in":600,"refresh_token":"r","token_type":"Bearer","username":"user1"}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "username"},
			want: partialIdentity{Subject: "user1"},
		},
		{
			name: "slack nested happy path",
			body: []byte(`{"ok":true,"access_token":"xoxb-...","authed_user":{"id":"U1234"},"team":{"id":"T1"}}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "authed_user.id"},
			want: partialIdentity{Subject: "U1234"},
		},
		{
			name: "shopify nested with all three fields",
			body: []byte(`{"access_token":"a","associated_user":{"id":902541635,"email":"john@example.com","first_name":"John"}}`),
			cfg: &IdentityFromTokenConfig{
				SubjectPath: "associated_user.id",
				NamePath:    "associated_user.first_name",
				EmailPath:   "associated_user.email",
			},
			want: partialIdentity{Subject: "902541635", Name: "John", Email: "john@example.com"},
		},
		{
			name: "numeric subject explicit",
			body: []byte(`{"user_id":42}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "user_id"},
			want: partialIdentity{Subject: "42"},
		},
		{
			name: "missing optional name path in body",
			body: []byte(`{"sub":"user1"}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "sub", NamePath: "display_name"},
			want: partialIdentity{Subject: "user1"},
		},
		{
			name: "name path not configured",
			body: []byte(`{"sub":"user1","display_name":"Alice"}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "sub"},
			want: partialIdentity{Subject: "user1"},
		},
		{
			name: "optional name path resolves to object, skipped",
			body: []byte(`{"sub":"u1","profile":{"first":"Alice"}}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "sub", NamePath: "profile"},
			want: partialIdentity{Subject: "u1"},
		},
		{
			name: "jwt-embedded subject happy path",
			body: []byte(`{"access_token":"` + makeJWT(`{"sub":"u1"}`) + `"}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "access_token|@upstreamjwt|sub"},
			want: partialIdentity{Subject: "u1"},
		},
		{
			name:      "jwt-embedded subject, malformed jwt",
			body:      []byte(`{"access_token":"not.a.jwt.really"}`),
			cfg:       &IdentityFromTokenConfig{SubjectPath: "access_token|@upstreamjwt|sub"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name:      "empty subject value",
			body:      []byte(`{"username":""}`),
			cfg:       &IdentityFromTokenConfig{SubjectPath: "username"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name:      "subject path resolves to object",
			body:      []byte(`{"user":{"id":"x"}}`),
			cfg:       &IdentityFromTokenConfig{SubjectPath: "user"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name:      "subject path resolves to null",
			body:      []byte(`{"username":null}`),
			cfg:       &IdentityFromTokenConfig{SubjectPath: "username"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name: "large numeric subject preserves integer precision beyond 2^53",
			// 9007199254740993 = 2^53 + 1; not exactly representable as float64.
			// We use the raw JSON token rather than formatting via gjson.Result.String()
			// (which goes through float64) to keep the digits exact.
			body: []byte(`{"user_id":9007199254740993}`),
			cfg:  &IdentityFromTokenConfig{SubjectPath: "user_id"},
			want: partialIdentity{Subject: "9007199254740993"},
		},
		{
			name:      "subject path missing from body",
			body:      []byte(`{}`),
			cfg:       &IdentityFromTokenConfig{SubjectPath: "username"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name:      "empty body",
			body:      []byte{},
			cfg:       &IdentityFromTokenConfig{SubjectPath: "username"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name:      "malformed json body",
			body:      []byte("not-json"),
			cfg:       &IdentityFromTokenConfig{SubjectPath: "username"},
			wantErr:   true,
			wantErrIs: ErrIdentityResolutionFailed,
		},
		{
			name:    "nil cfg",
			body:    []byte(`{"username":"user1"}`),
			cfg:     nil,
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := extractIdentityFromTokenResponse(tc.body, tc.cfg)
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantErrIs != nil {
					assert.True(t, errors.Is(err, tc.wantErrIs), "expected error to wrap %v, got: %v", tc.wantErrIs, err)
				}
				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExtractIdentityFromTokenResponse_ErrorDoesNotLeakBody(t *testing.T) {
	t.Parallel()

	const secretMarker = "DO-NOT-LEAK-ME-XYZ"
	body := []byte(`{"username":"","secret":"` + secretMarker + `"}`)

	_, err := extractIdentityFromTokenResponse(body, &IdentityFromTokenConfig{SubjectPath: "username"})
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), secretMarker),
		"error message must not contain body content, but got: %s", err.Error())
}
