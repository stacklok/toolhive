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

// Package server provides the OAuth 2.0 authorization server implementation for ToolHive.
//
// This package implements a standards-compliant OAuth 2.0 authorization server that acts
// as an intermediary between MCP clients and upstream identity providers (IdPs). It issues
// its own JWTs while federating authentication to external IdPs.
//
// # Architecture
//
// The server package is organized into focused sub-packages:
//
//   - server/registration: OAuth client types including RFC 8252 compliant LoopbackClient
//     for native applications with dynamic port matching
//   - server/crypto: Cryptographic utilities for key loading, PKCE, and signing
//   - server/session: Session management linking issued tokens to upstream IdP tokens
//
// # Protocol Compliance
//
// This implementation follows these OAuth 2.0 and OIDC specifications:
//
//   - RFC 6749: OAuth 2.0 Authorization Framework
//   - RFC 6750: Bearer Token Usage
//   - RFC 7636: Proof Key for Code Exchange (PKCE)
//   - RFC 7591: OAuth 2.0 Dynamic Client Registration
//   - RFC 8252: OAuth 2.0 for Native Apps (loopback redirect URI handling)
//   - OpenID Connect Core 1.0: Discovery and JWT claims
//
// # Main Entry Points
//
// For creating an authorization server configuration:
//
//	params := &server.AuthorizationServerParams{
//	    Issuer:              "https://auth.example.com",
//	    AccessTokenLifespan: time.Hour,
//	    SigningKeyID:        "key-1",
//	    SigningKeyAlgorithm: "RS256",
//	    SigningKey:          privateKey,
//	    HMACSecrets:         &crypto.HMACSecrets{
//	        Current: currentSecret,       // Active secret for signing
//	        Rotated: [][]byte{oldSecret}, // Previous secrets for verification (optional)
//	    },
//	}
//	authServerConfig, err := server.NewAuthorizationServerConfig(params)
//
// For creating OAuth clients:
//
//	client, err := registration.New(registration.Config{
//	    ID:           "my-client",
//	    RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
//	    Public:       true,
//	})
//
// # Token Flow
//
// The authorization server implements the authorization code flow with PKCE:
//
//  1. Client initiates auth at /oauth/authorize with PKCE challenge
//  2. Server redirects to upstream IdP for authentication
//  3. IdP calls back to /oauth/callback with auth code
//  4. Server exchanges code with IdP and stores IdP tokens
//  5. Server issues its own auth code and redirects to client
//  6. Client exchanges code at /oauth/token for JWT access token
//  7. JWT contains "tsid" claim linking to stored IdP tokens
//
// # Sub-package Details
//
// Use the sub-packages directly for more granular control:
//
//	import "github.com/stacklok/toolhive/pkg/authserver/server/registration"   // Client types
//	import "github.com/stacklok/toolhive/pkg/authserver/server/crypto"   // Key loading, PKCE
//	import "github.com/stacklok/toolhive/pkg/authserver/server/session"  // Session types
package server
