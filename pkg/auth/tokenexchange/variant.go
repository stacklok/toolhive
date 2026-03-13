// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
)

// VariantHandler defines the interface for pluggable token exchange variant
// implementations. Each variant can customize how the token exchange request
// is constructed and how the response is validated.
//
// The default (RFC 8693) handler is used when no variant is specified.
// Named variants (e.g., "entra") register themselves via init() and can
// derive configuration from provider-specific parameters.
type VariantHandler interface {
	// ResolveTokenURL is called once at config parse time. It returns a
	// resolved token endpoint URL derived from variant-specific parameters,
	// or an empty string to keep the ExchangeConfig.TokenURL as-is.
	// Named variants such as Entra use this to derive the URL from
	// parameters like tenantId.
	//
	// SECURITY: The returned URL is used as the target of an HTTP POST
	// carrying OAuth credentials. Implementations MUST return a fully
	// qualified HTTPS URL or an empty string. Callers SHOULD validate
	// the returned URL before use.
	ResolveTokenURL(config *ExchangeConfig) (string, error)

	// BuildFormData constructs the url.Values for the token exchange HTTP
	// POST request. The subjectToken parameter is the incoming user's token
	// to be exchanged. Implementations MUST NOT mutate the config parameter.
	//
	// SECURITY: Implementations MUST NOT log, return in error messages, or
	// otherwise expose the subjectToken value. It is a bearer credential.
	BuildFormData(config *ExchangeConfig, subjectToken string) (url.Values, error)

	// ValidateResponse checks variant-specific response fields after the
	// HTTP response has been parsed into a response struct. Implementations
	// should return an error if required fields are missing or invalid.
	ValidateResponse(resp *Response) error
}

// VariantRegistry holds a set of named VariantHandler implementations.
// It is safe for concurrent use. Tests should create isolated instances
// via NewVariantRegistry rather than using the global DefaultVariantRegistry.
type VariantRegistry struct {
	mu       sync.RWMutex
	variants map[string]VariantHandler
}

// NewVariantRegistry creates an empty VariantRegistry. Use this in tests
// to get an isolated registry that does not share state with production code.
func NewVariantRegistry() *VariantRegistry {
	return &VariantRegistry{
		variants: make(map[string]VariantHandler),
	}
}

// Register adds a VariantHandler under the given variant name.
// It panics if variant is empty, handler is nil, or a handler is already
// registered for that name. This follows the database/sql.Register pattern:
// double registration is always a programming error.
// Variant names are case-insensitive (normalized to lowercase).
func (r *VariantRegistry) Register(variant string, handler VariantHandler) {
	if variant == "" {
		panic("tokenexchange: Register variant name must not be empty")
	}
	if handler == nil {
		panic("tokenexchange: Register handler must not be nil")
	}

	variant = strings.ToLower(variant)

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.variants[variant]; exists {
		panic(fmt.Sprintf("tokenexchange: duplicate variant registration: %q", variant))
	}
	r.variants[variant] = handler
}

// Get retrieves the VariantHandler registered under the given variant name.
// It returns the handler and true if found, or nil and false otherwise.
// Variant names are case-insensitive (normalized to lowercase).
func (r *VariantRegistry) Get(variant string) (VariantHandler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.variants[strings.ToLower(variant)]
	return h, ok
}

// DefaultVariantRegistry is the production-global registry. Named variant
// implementations register themselves here from init() functions.
var DefaultVariantRegistry = NewVariantRegistry()

// RegisterVariantHandler is a convenience function that registers a handler
// in the DefaultVariantRegistry. It is intended for use in init() functions.
func RegisterVariantHandler(variant string, handler VariantHandler) {
	DefaultVariantRegistry.Register(variant, handler)
}

// defaultHandler is the built-in RFC 8693 handler used when no variant is
// specified in ExchangeConfig. It is NOT registered in any registry; it is
// the hardcoded fallback.
var defaultHandler VariantHandler = &rfc8693Handler{}

// rfc8693Handler implements VariantHandler for standard RFC 8693 token exchange.
// It produces form data that conforms to the specification and validates that
// the response contains the required issued_token_type field.
type rfc8693Handler struct{}

// ResolveTokenURL returns an empty string because RFC 8693 always uses the
// token URL provided directly in ExchangeConfig.TokenURL.
func (*rfc8693Handler) ResolveTokenURL(config *ExchangeConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("token exchange: config must not be nil")
	}
	return "", nil
}

// BuildFormData constructs the url.Values for a standard RFC 8693 token
// exchange request. It sets the required grant_type, subject_token,
// subject_token_type, and requested_token_type fields, plus optional
// audience and scope fields from the config.
//
// NOTE: RFC 8693 also defines optional "resource" (Section 2.1) and
// "actor_token"/"actor_token_type" (Section 2.1) parameters. These are
// not yet included because ExchangeConfig does not have Resource or
// ActingParty fields. When those fields are added to ExchangeConfig,
// this method should be updated to emit them.
func (*rfc8693Handler) BuildFormData(config *ExchangeConfig, subjectToken string) (url.Values, error) {
	if config == nil {
		return nil, fmt.Errorf("token exchange: config must not be nil")
	}
	if subjectToken == "" {
		return nil, fmt.Errorf("token exchange: subject_token is required")
	}

	data := url.Values{}

	data.Set("grant_type", grantTypeTokenExchange)
	data.Set("subject_token", subjectToken)

	// Subject token type defaults to access_token if not specified.
	subjectTokenType := config.SubjectTokenType
	if subjectTokenType == "" {
		subjectTokenType = tokenTypeAccessToken
	}
	data.Set("subject_token_type", subjectTokenType)

	// Always request access_token. This matches the default in the legacy
	// exchangeRequest path (exchange.go). If a future use case needs a
	// different requested token type, add a RequestedTokenType field to
	// ExchangeConfig and read it here.
	data.Set("requested_token_type", tokenTypeAccessToken)

	// Optional fields.
	if config.Audience != "" {
		data.Set("audience", config.Audience)
	}
	if len(config.Scopes) > 0 {
		data.Set("scope", strings.Join(config.Scopes, " "))
	}

	return data, nil
}

// ValidateResponse checks that the response contains issued_token_type,
// which is required by RFC 8693 Section 2.2.1.
func (*rfc8693Handler) ValidateResponse(resp *Response) error {
	if resp == nil {
		return fmt.Errorf("token exchange: response must not be nil")
	}
	if resp.IssuedTokenType == "" {
		return fmt.Errorf("token exchange: server returned empty issued_token_type (required by RFC 8693)")
	}
	return nil
}
