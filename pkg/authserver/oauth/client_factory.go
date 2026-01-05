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

import "github.com/ory/fosite"

// DefaultScopes are the default OIDC scopes for registered clients.
var DefaultScopes = []string{"openid", "profile", "email"}

// ClientConfig holds configuration for creating a new OAuth client.
type ClientConfig struct {
	// ID is the unique client identifier.
	ID string

	// Secret is the client secret for confidential clients.
	// Empty for public clients.
	Secret string

	// RedirectURIs is the list of allowed redirect URIs.
	RedirectURIs []string

	// Public indicates whether this is a public client (no secret).
	Public bool

	// GrantTypes overrides the default grant types.
	// If nil or empty, DefaultGrantTypes is used.
	GrantTypes []string

	// ResponseTypes overrides the default response types.
	// If nil or empty, DefaultResponseTypes is used.
	ResponseTypes []string

	// Scopes overrides the default scopes.
	// If nil or empty, DefaultScopes is used.
	Scopes []string
}

// NewClient creates a fosite.Client from the given configuration.
// Public clients are wrapped in LoopbackClient to support RFC 8252 Section 7.3
// compliant loopback redirect URI matching for native OAuth clients.
// Confidential clients with secrets have their Secret field set.
func NewClient(cfg ClientConfig) fosite.Client {
	// Apply defaults for empty slices
	grantTypes := cfg.GrantTypes
	if len(grantTypes) == 0 {
		grantTypes = DefaultGrantTypes
	}

	responseTypes := cfg.ResponseTypes
	if len(responseTypes) == 0 {
		responseTypes = DefaultResponseTypes
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}

	// Create the DefaultClient
	defaultClient := &fosite.DefaultClient{
		ID:            cfg.ID,
		RedirectURIs:  cfg.RedirectURIs,
		ResponseTypes: responseTypes,
		GrantTypes:    grantTypes,
		Scopes:        scopes,
		Public:        cfg.Public,
	}

	// Set secret for confidential clients
	if !cfg.Public && cfg.Secret != "" {
		defaultClient.Secret = []byte(cfg.Secret)
	}

	// Wrap public clients in LoopbackClient for RFC 8252 Section 7.3
	// dynamic port matching for native app loopback redirect URIs.
	if cfg.Public {
		return NewLoopbackClient(defaultClient)
	}

	return defaultClient
}
