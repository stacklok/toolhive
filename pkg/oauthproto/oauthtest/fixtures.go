// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oauthtest provides shared test fixtures for OAuth 2.0 response
// decoding. It is intended for use by tests in pkg/oauth and by sibling
// grant packages (token exchange, JWT bearer, etc.) so they share a single
// canonical response-builder rather than each maintaining a parallel copy
// that can drift.
package oauthtest

import "encoding/json"

// ResponseBuilder composes a JSON-encoded OAuth 2.0 token endpoint success
// response. Fluent setters leave unset fields as their zero value, matching
// the behavior of a minimal IdP reply. Build returns the marshaled JSON
// bytes ready to write into an httptest response.
type ResponseBuilder struct {
	AccessToken     string `json:"access_token,omitempty"`
	TokenType       string `json:"token_type,omitempty"`
	ExpiresIn       int    `json:"expires_in,omitempty"`
	RefreshToken    string `json:"refresh_token,omitempty"`
	IssuedTokenType string `json:"issued_token_type,omitempty"`
	Scope           string `json:"scope,omitempty"`
}

// NewResponse returns a builder pre-populated with a minimal valid RFC 8693
// success response (access token, Bearer type, access-token URN for issued
// type). Tests override any field they care about via the With* setters.
//
//nolint:gosec // G101: literal test-fixture value, not a real credential.
func NewResponse() *ResponseBuilder {
	return &ResponseBuilder{
		AccessToken:     "test-access-token",
		TokenType:       "Bearer",
		IssuedTokenType: "urn:ietf:params:oauth:token-type:access_token",
	}
}

// WithAccessToken overrides the access_token field (including the empty
// string, which lets tests exercise RFC 6749 §5.1 validation).
func (b *ResponseBuilder) WithAccessToken(token string) *ResponseBuilder {
	b.AccessToken = token
	return b
}

// WithTokenType overrides the token_type field.
func (b *ResponseBuilder) WithTokenType(tokenType string) *ResponseBuilder {
	b.TokenType = tokenType
	return b
}

// WithExpiresIn sets the expires_in field. Zero suppresses the field
// (omitempty) so callers can assert the no-expiry path.
func (b *ResponseBuilder) WithExpiresIn(seconds int) *ResponseBuilder {
	b.ExpiresIn = seconds
	return b
}

// WithRefreshToken sets the refresh_token field.
func (b *ResponseBuilder) WithRefreshToken(token string) *ResponseBuilder {
	b.RefreshToken = token
	return b
}

// WithIssuedTokenType sets the RFC 8693 issued_token_type field.
func (b *ResponseBuilder) WithIssuedTokenType(tokenType string) *ResponseBuilder {
	b.IssuedTokenType = tokenType
	return b
}

// WithScope sets the scope field (space-separated).
func (b *ResponseBuilder) WithScope(scope string) *ResponseBuilder {
	b.Scope = scope
	return b
}

// Build marshals the builder to JSON. Panics on marshaling errors; the
// underlying types are simple and failure indicates a programming error
// in the test itself.
func (b *ResponseBuilder) Build() []byte {
	out, err := json.Marshal(b)
	if err != nil {
		panic("oauthtest: marshal response: " + err.Error())
	}
	return out
}

// ErrorResponseBuilder composes a JSON-encoded OAuth 2.0 error response per
// RFC 6749 Section 5.2. Fluent setters populate only the listed fields —
// unset fields are omitted from the JSON output so callers can simulate
// minimal servers that return only the required error code.
type ErrorResponseBuilder struct {
	ErrorCode        string `json:"error,omitempty"`
	ErrorDescription string `json:"error_description,omitempty"`
	ErrorURI         string `json:"error_uri,omitempty"`
}

// NewErrorResponse returns an empty builder. Tests call WithError at minimum
// to produce a valid RFC 6749 §5.2 body.
func NewErrorResponse() *ErrorResponseBuilder {
	return &ErrorResponseBuilder{}
}

// WithError sets the error code (RFC 6749 §5.2 required field).
func (b *ErrorResponseBuilder) WithError(code string) *ErrorResponseBuilder {
	b.ErrorCode = code
	return b
}

// WithDescription sets error_description.
func (b *ErrorResponseBuilder) WithDescription(description string) *ErrorResponseBuilder {
	b.ErrorDescription = description
	return b
}

// WithURI sets error_uri.
func (b *ErrorResponseBuilder) WithURI(uri string) *ErrorResponseBuilder {
	b.ErrorURI = uri
	return b
}

// Build marshals the builder to JSON. Panics on marshaling errors; the
// underlying types are simple and failure indicates a programming error
// in the test itself.
func (b *ErrorResponseBuilder) Build() []byte {
	out, err := json.Marshal(b)
	if err != nil {
		panic("oauthtest: marshal error response: " + err.Error())
	}
	return out
}
