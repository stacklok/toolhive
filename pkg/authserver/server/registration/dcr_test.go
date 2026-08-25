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

package registration

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"math/big"
	"strings"
	"testing"

	"github.com/go-jose/go-jose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestValidateRedirectURI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uri         string
		expectError bool
		errorCode   string
	}{
		// HTTPS - allowed for any host
		{
			name:        "https with any host",
			uri:         "https://example.com/callback",
			expectError: false,
		},
		{
			name:        "https with custom domain",
			uri:         "https://myapp.example.org:8443/oauth/callback",
			expectError: false,
		},

		// HTTP loopback addresses - allowed per RFC 8252
		{
			name:        "http with 127.0.0.1",
			uri:         "http://127.0.0.1/callback",
			expectError: false,
		},
		{
			name:        "http with 127.0.0.1 and port",
			uri:         "http://127.0.0.1:8080/callback",
			expectError: false,
		},
		{
			name:        "http with localhost",
			uri:         "http://localhost/callback",
			expectError: false,
		},
		{
			name:        "http with localhost and port",
			uri:         "http://localhost:9000/callback",
			expectError: false,
		},

		// HTTP non-loopback - not allowed
		{
			name:        "http with non-loopback host",
			uri:         "http://example.com/callback",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name:        "http with IP address that is not loopback",
			uri:         "http://192.168.1.1/callback",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Invalid URI format
		{
			name:        "invalid URI format - missing scheme",
			uri:         "://invalid",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name:        "invalid URI format - malformed",
			uri:         "not a valid uri",
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Private-use URI schemes - allowed for native apps per RFC 8252 Section 7.1
		{
			name:        "custom scheme allowed for native apps",
			uri:         "myapp://callback",
			expectError: false,
		},
		{
			name:        "cursor scheme allowed",
			uri:         "cursor://callback",
			expectError: false,
		},
		{
			name:        "vscode scheme allowed",
			uri:         "vscode://callback",
			expectError: false,
		},

		// Length validation
		{
			name:        "redirect URI exceeding max length is rejected",
			uri:         "https://example.com/" + strings.Repeat("a", oauthproto.MaxRedirectURILength),
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateRedirectURI(tt.uri)

			if tt.expectError {
				require.NotNil(t, err, "expected error for URI %q", tt.uri)
				assert.Equal(t, tt.errorCode, err.Error)
			} else {
				assert.Nil(t, err, "unexpected error for URI %q: %v", tt.uri, err)
			}
		})
	}
}

func TestValidateDCRRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		request            *oauthproto.DynamicClientRegistrationRequest
		expectError        bool
		errorCode          string
		expectedAuthMethod string
		expectedGrants     []string
		expectedResponses  []string
	}{
		// Valid requests
		{
			name: "valid minimal request with loopback redirect URI",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
			},
			expectError:        false,
			expectedAuthMethod: "none",
			expectedGrants:     defaultGrantTypes,
			expectedResponses:  defaultResponseTypes,
		},
		{
			name: "valid request with all fields specified",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:            []string{"http://localhost:8080/callback", "https://example.com/callback"},
				ClientName:              "My Test Client",
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ResponseTypes:           []string{"code"},
			},
			expectError:        false,
			expectedAuthMethod: "none",
			expectedGrants:     []string{"authorization_code", "refresh_token"},
			expectedResponses:  []string{"code"},
		},
		{
			name: "valid request with https redirect URI",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"https://example.com/oauth/callback"},
			},
			expectError:        false,
			expectedAuthMethod: "none",
			expectedGrants:     defaultGrantTypes,
			expectedResponses:  defaultResponseTypes,
		},

		// Empty redirect_uris
		{
			name: "empty redirect_uris",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name: "nil redirect_uris",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: nil,
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Too many redirect URIs
		{
			name: "too many redirect URIs",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{
					"http://127.0.0.1:1/callback",
					"http://127.0.0.1:2/callback",
					"http://127.0.0.1:3/callback",
					"http://127.0.0.1:4/callback",
					"http://127.0.0.1:5/callback",
					"http://127.0.0.1:6/callback",
					"http://127.0.0.1:7/callback",
					"http://127.0.0.1:8/callback",
					"http://127.0.0.1:9/callback",
					"http://127.0.0.1:10/callback",
					"http://127.0.0.1:11/callback", // 11th - exceeds limit
				},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// Invalid redirect URI in list
		{
			name: "invalid redirect URI in list",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback", "http://example.com/callback"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},
		{
			name: "malformed redirect URI in list",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"://invalid"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidRedirectURI,
		},

		// token_endpoint_auth_method validation
		{
			name: "token_endpoint_auth_method = none",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "none",
			},
			expectError:        false,
			expectedAuthMethod: "none",
		},
		{
			name: "token_endpoint_auth_method empty defaults to none",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "",
			},
			expectError:        false,
			expectedAuthMethod: "none",
		},
		{
			name: "token_endpoint_auth_method = client_secret_basic fails",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "client_secret_basic",
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "token_endpoint_auth_method = client_secret_post fails",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:            []string{"http://127.0.0.1/callback"},
				TokenEndpointAuthMethod: "client_secret_post",
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},

		// grant_types validation
		{
			name: "grant_types defaults when empty",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{},
			},
			expectError:    false,
			expectedGrants: defaultGrantTypes,
		},
		{
			name: "grant_types defaults when nil",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   nil,
			},
			expectError:    false,
			expectedGrants: defaultGrantTypes,
		},
		{
			name: "grant_types without authorization_code fails",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"refresh_token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "grant_types with only client_credentials fails",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"client_credentials"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "grant_types with authorization_code passes",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"authorization_code"},
			},
			expectError:    false,
			expectedGrants: []string{"authorization_code"},
		},
		{
			name: "token exchange grant rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"authorization_code", oauthproto.GrantTypeTokenExchange},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "grant_types with unsupported type rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				GrantTypes:   []string{"authorization_code", "client_credentials"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},

		// response_types validation
		{
			name: "response_types defaults when empty",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{},
			},
			expectError:       false,
			expectedResponses: defaultResponseTypes,
		},
		{
			name: "response_types defaults when nil",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: nil,
			},
			expectError:       false,
			expectedResponses: defaultResponseTypes,
		},
		{
			name: "response_types without code fails",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "response_types with only id_token fails",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"id_token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "response_types with code passes",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"code"},
			},
			expectError:       false,
			expectedResponses: []string{"code"},
		},
		{
			name: "response_types with unsupported type rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs:  []string{"http://127.0.0.1/callback"},
				ResponseTypes: []string{"code", "token"},
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},

		// ClientName validation
		{
			name: "client_name is preserved",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				ClientName:   "My Application",
			},
			expectError: false,
		},
		{
			name: "client_name exceeding max length is rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				ClientName:   strings.Repeat("a", MaxClientNameLength+1),
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "client_name at max length is accepted",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				ClientName:   strings.Repeat("a", MaxClientNameLength),
			},
			expectError: false,
		},

		// software_id length cap and charset enforcement
		{
			name: "software_id at max length is accepted",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				SoftwareID:   strings.Repeat("a", MaxSoftwareIDLength),
			},
			expectError: false,
		},
		{
			name: "software_id exceeding max length is rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				SoftwareID:   strings.Repeat("a", MaxSoftwareIDLength+1),
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "software_id with control character is rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				SoftwareID:   "bad\x00id",
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "software_id with non-ASCII character is rejected",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				SoftwareID:   "softwäre",
			},
			expectError: true,
			errorCode:   DCRErrorInvalidClientMetadata,
		},
		{
			name: "empty software_id is accepted (field is optional)",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				SoftwareID:   "",
			},
			expectError: false,
		},
		{
			name: "printable-ASCII software_id is accepted",
			request: &oauthproto.DynamicClientRegistrationRequest{
				RedirectURIs: []string{"http://127.0.0.1/callback"},
				SoftwareID:   "example-app-v1.2.3",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := ValidateDCRRequest(tt.request, false, false)

			if tt.expectError {
				require.NotNil(t, err, "expected error")
				assert.Nil(t, result, "result should be nil on error")
				assert.Equal(t, tt.errorCode, err.Error)
			} else {
				require.Nil(t, err, "unexpected error: %v", err)
				require.NotNil(t, result, "result should not be nil on success")

				// Verify defaults/values were applied correctly
				if tt.expectedAuthMethod != "" {
					assert.Equal(t, tt.expectedAuthMethod, result.TokenEndpointAuthMethod)
				}
				if tt.expectedGrants != nil {
					assert.ElementsMatch(t, tt.expectedGrants, result.GrantTypes)
				}
				if tt.expectedResponses != nil {
					assert.ElementsMatch(t, tt.expectedResponses, result.ResponseTypes)
				}

				// Verify redirect_uris are preserved
				assert.Equal(t, tt.request.RedirectURIs, result.RedirectURIs)

				// Verify client_name is preserved
				assert.Equal(t, tt.request.ClientName, result.ClientName)

				// Verify software_id is preserved
				assert.Equal(t, tt.request.SoftwareID, result.SoftwareID)
			}
		})
	}
}

func TestValidatePrivateKeyJWTRegistration(t *testing.T) {
	t.Parallel()
	validKey := jose.JSONWebKey{Key: &rsa.PublicKey{N: big.NewInt(3), E: 3}, KeyID: "key-1", Algorithm: string(jose.RS256), Use: "sig"}
	request := func() *oauthproto.DynamicClientRegistrationRequest {
		return &oauthproto.DynamicClientRegistrationRequest{
			RedirectURIs:                []string{"https://client.example/callback"},
			TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
			JWKS:                        &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{validKey}},
			TokenEndpointAuthSigningAlg: string(jose.RS256),
		}
	}
	tests := []struct {
		name    string
		setup   func(*oauthproto.DynamicClientRegistrationRequest)
		allow   bool
		wantErr bool
	}{
		{"flag off", func(_ *oauthproto.DynamicClientRegistrationRequest) {}, false, true},
		{"valid inline JWKS", func(_ *oauthproto.DynamicClientRegistrationRequest) {}, true, false},
		{"missing JWKS", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKS = nil }, true, true},
		{"empty JWKS", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKS.Keys = nil }, true, true},
		{"malformed key", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKS.Keys[0] = jose.JSONWebKey{} }, true, true},
		{"symmetric key", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKS.Keys[0].Key = []byte("secret") }, true, true},
		{"private key", func(r *oauthproto.DynamicClientRegistrationRequest) {
			r.JWKS.Keys[0].Key = &rsa.PrivateKey{PublicKey: *validKey.Key.(*rsa.PublicKey), D: big.NewInt(1)}
		}, true, true},
		{"use mismatch", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKS.Keys[0].Use = "enc" }, true, true},
		{"algorithm mismatch", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKS.Keys[0].Algorithm = string(jose.ES256) }, true, true},
		{"unsupported algorithm", func(r *oauthproto.DynamicClientRegistrationRequest) { r.TokenEndpointAuthSigningAlg = "HS256" }, true, true},
		{"missing algorithm", func(r *oauthproto.DynamicClientRegistrationRequest) { r.TokenEndpointAuthSigningAlg = "" }, true, true},
		{"jwks_uri", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKSURI = "https://keys.example/jwks" }, true, true},
		{"both key sources", func(r *oauthproto.DynamicClientRegistrationRequest) { r.JWKSURI = "https://keys.example/jwks" }, true, true},
		{"wrong grant", func(r *oauthproto.DynamicClientRegistrationRequest) {
			r.GrantTypes = []string{oauthproto.GrantTypeAuthorizationCode}
		}, true, true},
		{"token exchange grant", func(r *oauthproto.DynamicClientRegistrationRequest) {
			r.GrantTypes = []string{oauthproto.GrantTypeTokenExchange}
		}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := request()
			tt.setup(r)
			got, err := ValidateDCRRequest(r, false, tt.allow)
			if tt.wantErr {
				require.Nil(t, got)
				require.NotNil(t, err)
				assert.Equal(t, DCRErrorInvalidClientMetadata, err.Error)
			} else {
				require.Nil(t, err)
				require.NotNil(t, got)
				assert.Equal(t, r.JWKS, got.JWKS)
			}
		})
	}
}
func TestPrivateKeyJWTAllowsTokenExchangeWithoutRedirectURI(t *testing.T) {
	t.Parallel()

	request := &oauthproto.DynamicClientRegistrationRequest{
		TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		TokenEndpointAuthSigningAlg: string(jose.RS256),
		GrantTypes:                  []string{oauthproto.GrantTypeTokenExchange},
		JWKS: &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &rsa.PublicKey{N: big.NewInt(3), E: 3}, KeyID: "key-1", Use: "sig",
		}}},
	}

	validated, err := ValidateDCRRequest(request, false, true)
	require.Nil(t, err)
	assert.Empty(t, validated.RedirectURIs)
	assert.Empty(t, validated.ResponseTypes)
}

func TestNonPrivateKeyJWTDropsJWKSMetadata(t *testing.T) {
	t.Parallel()

	validated, err := ValidateDCRRequest(&oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs:                []string{"http://127.0.0.1/callback"},
		JWKS:                        &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: []byte("secret")}}},
		JWKSURI:                     "https://keys.example/jwks",
		TokenEndpointAuthSigningAlg: "RS256",
	}, false, false)
	require.Nil(t, err)
	assert.Nil(t, validated.JWKS)
	assert.Empty(t, validated.JWKSURI)
	assert.Empty(t, validated.TokenEndpointAuthSigningAlg)
}

func TestPrivateKeyJWTAllowsJWKWithoutAlgorithm(t *testing.T) {
	t.Parallel()

	validated, err := ValidateDCRRequest(&oauthproto.DynamicClientRegistrationRequest{
		TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		TokenEndpointAuthSigningAlg: string(jose.RS256),
		GrantTypes:                  []string{oauthproto.GrantTypeTokenExchange},
		JWKS: &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: &rsa.PublicKey{N: big.NewInt(3), E: 3}, KeyID: "key-1", Use: "sig",
		}}},
	}, false, true)
	require.Nil(t, err)
	assert.Empty(t, validated.JWKS.Keys[0].Algorithm)
}

func TestPrivateKeyJWTAllowsEd25519Key(t *testing.T) {
	t.Parallel()

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	validated, dcrErr := ValidateDCRRequest(&oauthproto.DynamicClientRegistrationRequest{
		TokenEndpointAuthMethod:     oauthproto.TokenEndpointAuthMethodPrivateKeyJWT,
		TokenEndpointAuthSigningAlg: "EdDSA",
		GrantTypes:                  []string{oauthproto.GrantTypeTokenExchange},
		JWKS: &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: pub, KeyID: "key-1", Use: "sig", Algorithm: "EdDSA",
		}}},
	}, false, true)
	require.Nil(t, dcrErr)
	require.NotNil(t, validated)
}

func TestValidateScopes(t *testing.T) {
	t.Parallel()

	allowedScopes := []string{"openid", "profile", "email", "offline_access"}

	tests := []struct {
		name            string
		requestedScopes []string
		allowedScopes   []string
		expectError     bool
		errorCode       string
		expectedScopes  []string
	}{
		{
			name:            "valid subset of allowed scopes",
			requestedScopes: []string{"openid", "profile"},
			allowedScopes:   allowedScopes,
			expectedScopes:  []string{"openid", "profile"},
		},
		{
			name:            "full set of allowed scopes accepted",
			requestedScopes: []string{"openid", "profile", "email", "offline_access"},
			allowedScopes:   allowedScopes,
			expectedScopes:  []string{"openid", "profile", "email", "offline_access"},
		},
		{
			name:            "unknown scope rejected",
			requestedScopes: []string{"openid", "sneaky_admin"},
			allowedScopes:   allowedScopes,
			expectError:     true,
			errorCode:       DCRErrorInvalidClientMetadata,
		},
		{
			name:            "prefix of valid scope rejected",
			requestedScopes: []string{"openid.evil"},
			allowedScopes:   allowedScopes,
			expectError:     true,
			errorCode:       DCRErrorInvalidClientMetadata,
		},
		{
			name:            "substring of valid scope rejected",
			requestedScopes: []string{"open"},
			allowedScopes:   allowedScopes,
			expectError:     true,
			errorCode:       DCRErrorInvalidClientMetadata,
		},
		{
			name:            "empty input returns default scopes",
			requestedScopes: nil,
			allowedScopes:   allowedScopes,
			expectedScopes:  DefaultScopes,
		},
		{
			name:            "duplicate scopes are deduplicated",
			requestedScopes: []string{"openid", "openid", "profile"},
			allowedScopes:   allowedScopes,
			expectedScopes:  []string{"openid", "profile"},
		},
		{
			name:            "empty input rejected when defaults not in allowed set",
			requestedScopes: nil,
			allowedScopes:   []string{"custom_scope"},
			expectError:     true,
			errorCode:       DCRErrorInvalidClientMetadata,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			scopes, dcrErr := ValidateScopes(tt.requestedScopes, tt.allowedScopes)

			if tt.expectError {
				require.NotNil(t, dcrErr, "expected error")
				assert.Equal(t, tt.errorCode, dcrErr.Error)
				assert.Nil(t, scopes)
			} else {
				require.Nil(t, dcrErr, "unexpected error: %v", dcrErr)
				assert.Equal(t, tt.expectedScopes, scopes)
			}
		})
	}
}

func TestDCRErrorConstants(t *testing.T) {
	t.Parallel()

	// Verify error code constants match RFC 7591 Section 3.2.2
	assert.Equal(t, "invalid_redirect_uri", DCRErrorInvalidRedirectURI)
	assert.Equal(t, "invalid_client_metadata", DCRErrorInvalidClientMetadata)
}

func TestDefaultGrantTypesAndResponseTypes(t *testing.T) {
	t.Parallel()

	// Verify default grant types include authorization_code
	assert.Contains(t, defaultGrantTypes, "authorization_code")
	assert.Contains(t, defaultGrantTypes, "refresh_token")

	// Verify default response types include code
	assert.Contains(t, defaultResponseTypes, "code")
}

func TestFilterPublicGrantTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		grantTypes []string
		want       []string
		wantErr    bool
	}{
		{"nil gets defaults", nil, []string{"authorization_code", "refresh_token"}, false},
		{"empty gets defaults", []string{}, []string{"authorization_code", "refresh_token"}, false},
		{"supported set passes through", []string{"authorization_code", "refresh_token"},
			[]string{"authorization_code", "refresh_token"}, false},
		{"unsupported entries are dropped, not fatal",
			[]string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
			[]string{"authorization_code", "refresh_token"}, false},
		{"unsupported-only list rejected (authorization_code missing)",
			[]string{"urn:ietf:params:oauth:grant-type:device_code", "client_credentials"}, nil, true},
		{"refresh_token only rejected (authorization_code missing)", []string{"refresh_token"}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, dcrErr := FilterPublicGrantTypes(tt.grantTypes)
			if tt.wantErr {
				require.NotNil(t, dcrErr)
				assert.Equal(t, DCRErrorInvalidClientMetadata, dcrErr.Error)
				return
			}
			require.Nil(t, dcrErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFilterPublicResponseTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		responseTypes []string
		want          []string
		wantErr       bool
	}{
		{"nil gets defaults", nil, []string{"code"}, false},
		{"code passes through", []string{"code"}, []string{"code"}, false},
		{"unsupported entries are dropped, not fatal", []string{"code", "token"}, []string{"code"}, false},
		{"unsupported-only list rejected (code missing)", []string{"token"}, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, dcrErr := FilterPublicResponseTypes(tt.responseTypes)
			if tt.wantErr {
				require.NotNil(t, dcrErr)
				assert.Equal(t, DCRErrorInvalidClientMetadata, dcrErr.Error)
				return
			}
			require.Nil(t, dcrErr)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestValidateScopeSubset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		subset    []string
		superset  []string
		fieldName string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "nil subset passes",
			subset:    nil,
			superset:  []string{"openid", "profile"},
			fieldName: "baseline_client_scopes",
		},
		{
			name:      "empty subset passes",
			subset:    []string{},
			superset:  []string{"openid", "profile"},
			fieldName: "baseline_client_scopes",
		},
		{
			name:      "all subset entries present in superset passes",
			subset:    []string{"openid", "profile"},
			superset:  []string{"openid", "profile", "email", "offline_access"},
			fieldName: "baseline_client_scopes",
		},
		{
			name:      "single entry not in superset returns error",
			subset:    []string{"offline_access"},
			superset:  []string{"openid"},
			fieldName: "baseline_client_scopes",
			wantErr:   true,
			errMsg:    `baseline_client_scopes contains "offline_access" which is not in scopes_supported`,
		},
		{
			name:      "first offending entry reported in error",
			subset:    []string{"foo", "bar"},
			superset:  []string{"openid"},
			fieldName: "baseline_client_scopes",
			wantErr:   true,
			errMsg:    `baseline_client_scopes contains "foo" which is not in scopes_supported`,
		},
		{
			name:      "non-nil subset with nil superset returns error",
			subset:    []string{"openid"},
			superset:  nil,
			fieldName: "baseline_client_scopes",
			wantErr:   true,
			errMsg:    `baseline_client_scopes contains "openid" which is not in scopes_supported`,
		},
		{
			name:      "fieldName is embedded in error message",
			subset:    []string{"missing"},
			superset:  []string{"openid"},
			fieldName: "my_custom_field",
			wantErr:   true,
			errMsg:    `my_custom_field contains "missing" which is not in scopes_supported`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateScopeSubset(tt.subset, tt.superset, tt.fieldName)

			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestUnionScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		req      []string
		baseline []string
		want     []string
	}{
		{name: "both nil returns nil", req: nil, baseline: nil, want: nil},
		{name: "both empty returns nil", req: []string{}, baseline: []string{}, want: nil},
		{name: "requested only preserved unchanged", req: []string{"openid", "profile"}, baseline: nil, want: []string{"openid", "profile"}},
		{name: "baseline only returned when no requested", req: nil, baseline: []string{"openid", "email"}, want: []string{"openid", "email"}},
		{name: "requested subset of baseline expands correctly", req: []string{"openid"}, baseline: []string{"openid", "profile", "email"}, want: []string{"openid", "profile", "email"}},
		{name: "disjoint sets: requested first then baseline", req: []string{"openid", "profile"}, baseline: []string{"email", "offline_access"}, want: []string{"openid", "profile", "email", "offline_access"}},
		{name: "exact match produces no duplicates", req: []string{"openid", "profile"}, baseline: []string{"openid", "profile"}, want: []string{"openid", "profile"}},
		{name: "duplicates in requested are deduplicated", req: []string{"openid", "openid", "profile"}, baseline: nil, want: []string{"openid", "profile"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := UnionScopes(tt.req, tt.baseline)
			assert.Equal(t, tt.want, got)
		})
	}
}
