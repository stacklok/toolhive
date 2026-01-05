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

package idp

import (
	"context"
)

// UpstreamConfig contains configuration for connecting to an upstream
// Identity Provider. This is a parallel struct definition that mirrors
// the authserver package's UpstreamConfig to avoid import cycles.
type UpstreamConfig struct {
	// Issuer is the URL of the upstream IDP (e.g., https://accounts.google.com).
	Issuer string

	// ClientID is the OAuth client ID registered with the upstream IDP.
	ClientID string

	// ClientSecret is the OAuth client secret registered with the upstream IDP.
	ClientSecret string

	// Scopes are the OAuth scopes to request from the upstream IDP.
	Scopes []string

	// RedirectURI is the callback URL where the upstream IDP will redirect
	// after authentication. This should be our authorization server's callback endpoint.
	RedirectURI string
}

// NewFromConfig creates an IDP provider from UpstreamConfig.
// Returns nil, nil if upstream is not configured (nil or empty Issuer).
func NewFromConfig(ctx context.Context, upstream *UpstreamConfig) (Provider, error) {
	if upstream == nil || upstream.Issuer == "" {
		return nil, nil
	}

	cfg := Config{
		Issuer:       upstream.Issuer,
		ClientID:     upstream.ClientID,
		ClientSecret: upstream.ClientSecret,
		Scopes:       upstream.Scopes,
		RedirectURI:  upstream.RedirectURI,
	}

	return NewOIDCProvider(ctx, cfg)
}
