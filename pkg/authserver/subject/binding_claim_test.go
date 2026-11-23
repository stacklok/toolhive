// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package subject

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateBindingClaim(t *testing.T) {
	t.Parallel()

	const (
		genericIssuer    = "https://login.example.com"
		oktaCustomIssuer = "https://dev-123456.okta.com/oauth2/default"
		oktaOrgIssuer    = "https://dev-1.okta.com"
		oktaUpperIssuer  = "https://DEV-1.OKTA.COM/oauth2/default"
	)

	tests := []struct {
		name        string
		issuer      string
		claim       string
		wantErr     bool
		wantWarning bool
	}{
		{name: "email is refused", issuer: genericIssuer, claim: "email", wantErr: true},
		{name: "preferred_username is refused", issuer: genericIssuer, claim: "preferred_username", wantErr: true},
		{name: "upn is refused", issuer: genericIssuer, claim: "upn", wantErr: true},
		{name: "unique_name is refused", issuer: genericIssuer, claim: "unique_name", wantErr: true},
		{name: "name is refused", issuer: genericIssuer, claim: "name", wantErr: true},
		{name: "mail is refused", issuer: genericIssuer, claim: "mail", wantErr: true},
		{name: "username is refused", issuer: genericIssuer, claim: "username", wantErr: true},
		{name: "phone_number is refused", issuer: genericIssuer, claim: "phone_number", wantErr: true},
		{name: "Given_Name is refused", issuer: genericIssuer, claim: "Given_Name", wantErr: true},
		{name: "Email is refused", issuer: genericIssuer, claim: "Email", wantErr: true},
		{name: "leading space is refused", issuer: genericIssuer, claim: " email", wantErr: true},
		{name: "trailing space is refused", issuer: genericIssuer, claim: "email ", wantErr: true},
		{name: "empty claim is refused", issuer: genericIssuer, claim: "", wantErr: true},
		{name: "oid is allowed", issuer: genericIssuer, claim: "oid"},
		{name: "sub on Okta custom authorization server warns", issuer: oktaCustomIssuer, claim: "sub", wantWarning: true},
		{name: "sub on Okta custom authorization server with uppercase host warns", issuer: oktaUpperIssuer, claim: "sub", wantWarning: true},
		{name: "sub on the Okta org authorization server is allowed without warning", issuer: oktaOrgIssuer, claim: "sub"},
		{name: "sub on another issuer is allowed without warning", issuer: genericIssuer, claim: "sub"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			warning, err := ValidateBindingClaim(tc.issuer, tc.claim)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidBindingClaim)
			} else {
				require.NoError(t, err)
			}
			if tc.wantWarning {
				require.NotEmpty(t, warning)
			} else {
				require.Empty(t, warning)
			}
		})
	}
}
