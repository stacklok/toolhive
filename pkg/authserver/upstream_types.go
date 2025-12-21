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

package authserver

import (
	"context"
	"net/http"
)

// PKCEChallengeMethodS256 is the PKCE challenge method for SHA-256.
const PKCEChallengeMethodS256 = "S256"

// UpstreamProvider handles communication with an upstream Identity Provider.
type UpstreamProvider interface {
	// Name returns the provider name (e.g., "google", "oidc").
	Name() string

	// AuthorizationURL builds the URL to redirect the user to the upstream IDP.
	// state: our internal state to correlate callback
	// codeChallenge: PKCE challenge to send to upstream (if supported)
	// scopes: scopes to request from upstream
	AuthorizationURL(state, codeChallenge string, scopes []string) (string, error)

	// ExchangeCode exchanges an authorization code for tokens with the upstream IDP.
	ExchangeCode(ctx context.Context, code, codeVerifier string) (*IDPTokens, error)

	// RefreshTokens refreshes the upstream IDP tokens.
	RefreshTokens(ctx context.Context, refreshToken string) (*IDPTokens, error)

	// UserInfo fetches user information from the upstream IDP.
	UserInfo(ctx context.Context, accessToken string) (*UserInfo, error)
}

// HTTPClient is an interface for HTTP client operations.
// This allows for mocking in tests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}
