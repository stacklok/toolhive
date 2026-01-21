// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
