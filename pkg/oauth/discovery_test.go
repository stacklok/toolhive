// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package oauth

import (
	"errors"
	"testing"
)

func TestOIDCDiscoveryDocument_Validate(t *testing.T) {
	t.Parallel()

	validDoc := func() OIDCDiscoveryDocument {
		return OIDCDiscoveryDocument{
			AuthorizationServerMetadata: AuthorizationServerMetadata{
				Issuer:                 "https://example.com",
				AuthorizationEndpoint:  "https://example.com/authorize",
				TokenEndpoint:          "https://example.com/token",
				JWKSURI:                "https://example.com/jwks",
				ResponseTypesSupported: []string{"code"},
			},
		}
	}

	tests := []struct {
		name    string
		modify  func(*OIDCDiscoveryDocument)
		isOIDC  bool
		wantErr error
	}{
		{"valid OAuth document", nil, false, nil},
		{"valid OIDC document", nil, true, nil},
		{"missing issuer", func(d *OIDCDiscoveryDocument) { d.Issuer = "" }, false, ErrMissingIssuer},
		{"missing authorization_endpoint", func(d *OIDCDiscoveryDocument) { d.AuthorizationEndpoint = "" }, false, ErrMissingAuthorizationEndpoint},
		{"missing token_endpoint", func(d *OIDCDiscoveryDocument) { d.TokenEndpoint = "" }, false, ErrMissingTokenEndpoint},
		{"missing jwks_uri for OIDC", func(d *OIDCDiscoveryDocument) { d.JWKSURI = "" }, true, ErrMissingJWKSURI},
		{"missing jwks_uri for OAuth is OK", func(d *OIDCDiscoveryDocument) { d.JWKSURI = "" }, false, nil},
		{"missing response_types_supported for OIDC", func(d *OIDCDiscoveryDocument) { d.ResponseTypesSupported = nil }, true, ErrMissingResponseTypesSupported},
		{"missing response_types_supported for OAuth is OK", func(d *OIDCDiscoveryDocument) { d.ResponseTypesSupported = nil }, false, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := validDoc()
			if tt.modify != nil {
				tt.modify(&doc)
			}
			err := doc.Validate(tt.isOIDC)
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestOIDCDiscoveryDocument_SupportsPKCE(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		methods []string
		want    bool
	}{
		{"nil slice", nil, false},
		{"empty slice", []string{}, false},
		{"only plain", []string{"plain"}, false},
		{"S256 present", []string{"S256"}, true},
		{"both plain and S256", []string{"plain", "S256"}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := OIDCDiscoveryDocument{
				AuthorizationServerMetadata: AuthorizationServerMetadata{
					CodeChallengeMethodsSupported: tt.methods,
				},
			}
			if got := doc.SupportsPKCE(); got != tt.want {
				t.Errorf("SupportsPKCE() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOIDCDiscoveryDocument_SupportsGrantType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		grants    []string
		grantType string
		want      bool
	}{
		{"nil slice", nil, GrantTypeAuthorizationCode, false},
		{"empty slice", []string{}, GrantTypeAuthorizationCode, false},
		{"grant type present", []string{GrantTypeAuthorizationCode}, GrantTypeAuthorizationCode, true},
		{"grant type absent", []string{GrantTypeRefreshToken}, GrantTypeAuthorizationCode, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			doc := OIDCDiscoveryDocument{
				AuthorizationServerMetadata: AuthorizationServerMetadata{
					GrantTypesSupported: tt.grants,
				},
			}
			if got := doc.SupportsGrantType(tt.grantType); got != tt.want {
				t.Errorf("SupportsGrantType(%q) = %v, want %v", tt.grantType, got, tt.want)
			}
		})
	}
}
