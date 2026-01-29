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

import "errors"

// Validation errors for discovery documents.
var (
	// ErrMissingIssuer indicates the issuer field is missing from the discovery document.
	ErrMissingIssuer = errors.New("missing issuer")

	// ErrMissingAuthorizationEndpoint indicates the authorization_endpoint field is missing.
	ErrMissingAuthorizationEndpoint = errors.New("missing authorization_endpoint")

	// ErrMissingTokenEndpoint indicates the token_endpoint field is missing.
	ErrMissingTokenEndpoint = errors.New("missing token_endpoint")

	// ErrMissingJWKSURI indicates the jwks_uri field is missing (required for OIDC).
	ErrMissingJWKSURI = errors.New("missing jwks_uri")

	// ErrMissingResponseTypesSupported indicates the response_types_supported field is missing (required for OIDC).
	ErrMissingResponseTypesSupported = errors.New("missing response_types_supported")
)
