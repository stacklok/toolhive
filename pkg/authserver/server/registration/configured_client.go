// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"
	"time"

	"github.com/go-jose/go-jose/v3"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// defaultConfiguredSigningAlgorithm is the private_key_jwt signing algorithm
// a configured client uses when none is declared.
const defaultConfiguredSigningAlgorithm = "RS256"

// maxAllowedSourceClients bounds token_exchange.allowed_source_clients.
const maxAllowedSourceClients = 20

// Values of token_exchange.self_issued_subject_tokens. They mirror
// authserver.SelfIssuedSubjectTokens, which this package cannot import.
const (
	selfIssuedSubjectTokensAny        = "any"
	selfIssuedSubjectTokensRestricted = "restricted"
)

// maxExchangeRefreshTokenLifetime bounds each exchange_refresh_tokens lifetime cap.
const maxExchangeRefreshTokenLifetime = 720 * time.Hour

// configuredGrantTypes are the grants an operator-configured client may hold.
var configuredGrantTypes = []string{
	oauthproto.GrantTypeAuthorizationCode,
	oauthproto.GrantTypeRefreshToken,
	oauthproto.GrantTypeTokenExchange,
	oauthproto.GrantTypeJWTBearer,
}

// ConfiguredClientSpec is the client metadata ValidateConfiguredClient checks.
type ConfiguredClientSpec struct {
	// TokenEndpointAuthMethod is empty (unpinned client secret),
	// client_secret_basic, client_secret_post, or private_key_jwt.
	TokenEndpointAuthMethod string

	// SigningAlgorithm is the private_key_jwt algorithm. Empty means
	// defaultConfiguredSigningAlgorithm.
	SigningAlgorithm string

	// GrantTypes empty means the default [token-exchange].
	GrantTypes []string

	RedirectURIs []string

	// HasSecret reports whether a client secret is configured. It is required
	// unless the method is private_key_jwt, and forbidden with it.
	HasSecret bool

	// JWKS holds the loaded public keys.
	JWKS *jose.JSONWebKeySet

	// JWKSPending reports keys configured by reference but not loaded yet, so
	// the key contents cannot be checked.
	JWKSPending bool

	// Audiences are the client's own audiences. api_audience must be one of
	// them; the caller has already checked them against the server's allowed
	// audiences, so api_audience is within those too.
	Audiences []string

	// TokenExchange reports a token_exchange block.
	TokenExchange bool

	// SelfIssuedSubjectTokens is the block's trust mode, any or restricted.
	// It is required when TokenExchange is set.
	SelfIssuedSubjectTokens string

	// AllowedSourceClients and APIAudience are the token_exchange fields.
	// Both are valid only with the restricted mode.
	AllowedSourceClients []string
	APIAudience          string

	// ExchangeRefreshTokens reports an exchange_refresh_tokens block. It is
	// only meaningful with TokenExchange set, and valid only with the
	// restricted mode.
	ExchangeRefreshTokens bool

	// IDJAGMaxLifetime and RefreshTokenMaxLifetime are the block's lifetime
	// caps as written, as Go duration strings. Empty means not set.
	IDJAGMaxLifetime        string
	RefreshTokenMaxLifetime string

	// ClientID is the client's own client_id, which must not be listed as a
	// source client.
	ClientID string

	// DelegateClientIDs are the client_ids of every configured delegate
	// client. None may be an allowed source client.
	DelegateClientIDs []string
}

// effectiveConfiguredGrantTypes returns grantTypes, or the default
// [token-exchange] when none are declared.
func effectiveConfiguredGrantTypes(grantTypes []string) []string {
	if len(grantTypes) == 0 {
		return []string{oauthproto.GrantTypeTokenExchange}
	}
	return grantTypes
}

// ValidateConfiguredClient checks the metadata of an operator-configured
// client: the auth method, the key set, the grant set and the redirect URIs.
func ValidateConfiguredClient(spec ConfiguredClientSpec) error {
	if err := validateConfiguredAuthMethod(spec); err != nil {
		return err
	}
	if err := validateConfiguredKeys(spec); err != nil {
		return err
	}
	grants := effectiveConfiguredGrantTypes(spec.GrantTypes)
	if err := validateConfiguredGrants(grants); err != nil {
		return err
	}
	if err := validateConfiguredRedirects(spec.RedirectURIs, grants); err != nil {
		return err
	}
	if err := validateConfiguredTokenExchange(spec); err != nil {
		return err
	}
	if err := validateConfiguredExchangeRefreshTokens(spec); err != nil {
		return err
	}
	return validateConfiguredRedirectURIs(spec.RedirectURIs)
}

func validateConfiguredAuthMethod(spec ConfiguredClientSpec) error {
	method := spec.TokenEndpointAuthMethod
	switch method {
	case "",
		oauthproto.TokenEndpointAuthMethodClientSecretBasic,
		oauthproto.TokenEndpointAuthMethodClientSecretPost,
		oauthproto.TokenEndpointAuthMethodPrivateKeyJWT:
	default:
		return fmt.Errorf("unsupported token_endpoint_auth_method: %q", method)
	}
	usesKey := method == oauthproto.TokenEndpointAuthMethodPrivateKeyJWT
	if usesKey && spec.HasSecret {
		return fmt.Errorf("a client secret is not allowed with private_key_jwt")
	}
	if !usesKey && !spec.HasSecret {
		return fmt.Errorf("a client secret is required")
	}
	return nil
}

func validateConfiguredGrants(grants []string) error {
	seen := make(map[string]struct{}, len(grants))
	for _, grant := range grants {
		if !slices.Contains(configuredGrantTypes, grant) {
			return fmt.Errorf("unsupported grant type %q", grant)
		}
		if _, dup := seen[grant]; dup {
			return fmt.Errorf("duplicate grant type %q", grant)
		}
		seen[grant] = struct{}{}
	}
	if !slices.Contains(grants, oauthproto.GrantTypeTokenExchange) {
		return fmt.Errorf("grant_types must include %q", oauthproto.GrantTypeTokenExchange)
	}
	return nil
}

func validateConfiguredRedirects(redirectURIs, grants []string) error {
	if slices.Contains(grants, oauthproto.GrantTypeAuthorizationCode) {
		if len(redirectURIs) == 0 {
			return fmt.Errorf("redirect_uris is required with the authorization_code grant")
		}
	} else if len(redirectURIs) > 0 {
		return fmt.Errorf("redirect_uris is only allowed with the authorization_code grant")
	}
	if slices.Contains(grants, oauthproto.GrantTypeRefreshToken) &&
		!slices.Contains(grants, oauthproto.GrantTypeAuthorizationCode) {
		return fmt.Errorf("grant_types must include %q with %q",
			oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken)
	}
	return nil
}

func validateConfiguredRedirectURIs(redirectURIs []string) error {
	for _, uri := range redirectURIs {
		if err := oauthproto.ValidateRedirectURI(uri, oauthproto.RedirectURIPolicyStrict); err != nil {
			return fmt.Errorf("redirect_uris: %w", err)
		}
		if isLoopbackURI(uri) {
			return fmt.Errorf("redirect_uris must be https and not loopback (offending redirect_uri: %s)", uri)
		}
	}
	return nil
}

func validateConfiguredKeys(spec ConfiguredClientSpec) error {
	if spec.TokenEndpointAuthMethod != oauthproto.TokenEndpointAuthMethodPrivateKeyJWT {
		if spec.JWKS != nil || spec.JWKSPending {
			return fmt.Errorf("jwks_file is only allowed with token_endpoint_auth_method %q",
				oauthproto.TokenEndpointAuthMethodPrivateKeyJWT)
		}
		if spec.SigningAlgorithm != "" {
			return fmt.Errorf("token_endpoint_auth_signing_alg is only allowed with token_endpoint_auth_method %q",
				oauthproto.TokenEndpointAuthMethodPrivateKeyJWT)
		}
		return nil
	}

	if !spec.JWKSPending && (spec.JWKS == nil || len(spec.JWKS.Keys) == 0) {
		return fmt.Errorf("private_key_jwt requires jwks_file with at least one key")
	}
	alg := spec.SigningAlgorithm
	if alg == "" {
		alg = defaultConfiguredSigningAlgorithm
	}
	if spec.JWKSPending && spec.JWKS == nil {
		if !supportedSigningAlgorithm(alg) {
			return fmt.Errorf("token_endpoint_auth_signing_alg %q is unsupported", alg)
		}
		return nil
	}
	if dcrErr := validatePrivateKeyJWTKeySet(spec.JWKS, alg); dcrErr != nil {
		return fmt.Errorf("jwks_file: %s", dcrErr.ErrorDescription)
	}
	return nil
}

func validateConfiguredTokenExchange(spec ConfiguredClientSpec) error {
	if !spec.TokenExchange {
		return nil
	}
	if err := validateSelfIssuedSubjectTokens(spec); err != nil {
		return err
	}
	if len(spec.AllowedSourceClients) > maxAllowedSourceClients {
		return fmt.Errorf("token_exchange.allowed_source_clients has %d entries, at most %d are allowed",
			len(spec.AllowedSourceClients), maxAllowedSourceClients)
	}
	seen := make(map[string]struct{}, len(spec.AllowedSourceClients))
	for _, id := range spec.AllowedSourceClients {
		switch id {
		case "":
			return fmt.Errorf("token_exchange.allowed_source_clients must not contain an empty client_id")
		case "*":
			return fmt.Errorf("token_exchange.allowed_source_clients does not support the \"*\" wildcard")
		}
		if id == spec.ClientID {
			return fmt.Errorf("token_exchange.allowed_source_clients lists the client's own client_id %q: "+
				"its own tokens are always accepted, so it must not be listed", id)
		}
		if slices.Contains(spec.DelegateClientIDs, id) {
			return fmt.Errorf("token_exchange.allowed_source_clients lists %q, a configured delegate client: "+
				"source clients must be user-facing clients, and a delegate client's tokens carry its own client_id, "+
				"so listing it would trust everything it accepts", id)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("token_exchange.allowed_source_clients lists %q more than once", id)
		}
		seen[id] = struct{}{}
	}
	if spec.APIAudience != "" && !slices.Contains(spec.Audiences, spec.APIAudience) {
		return fmt.Errorf("token_exchange.api_audience %q is not one of the client's audiences", spec.APIAudience)
	}
	return nil
}

// validateSelfIssuedSubjectTokens checks the trust mode of a token_exchange
// block: it is required, and the source list and api_audience narrow a
// restricted client only, so they are refused with any.
func validateSelfIssuedSubjectTokens(spec ConfiguredClientSpec) error {
	switch spec.SelfIssuedSubjectTokens {
	case selfIssuedSubjectTokensRestricted:
		return nil
	case selfIssuedSubjectTokensAny:
		if len(spec.AllowedSourceClients) > 0 {
			return fmt.Errorf("token_exchange.allowed_source_clients is valid only with "+
				"self_issued_subject_tokens %q", selfIssuedSubjectTokensRestricted)
		}
		if spec.APIAudience != "" {
			return fmt.Errorf("token_exchange.api_audience is valid only with "+
				"self_issued_subject_tokens %q", selfIssuedSubjectTokensRestricted)
		}
		return nil
	case "":
		return fmt.Errorf("token_exchange.self_issued_subject_tokens is required: set %q or %q",
			selfIssuedSubjectTokensAny, selfIssuedSubjectTokensRestricted)
	default:
		return fmt.Errorf("token_exchange.self_issued_subject_tokens %q is not supported: set %q or %q",
			spec.SelfIssuedSubjectTokens, selfIssuedSubjectTokensAny, selfIssuedSubjectTokensRestricted)
	}
}

// validateConfiguredExchangeRefreshTokens checks the exchange_refresh_tokens
// block. The confidential-client and token-exchange-grant checks are also
// enforced for every configured client in ValidateConfiguredClient. They are
// kept here so the block stays gated if those client-wide rules are relaxed.
func validateConfiguredExchangeRefreshTokens(spec ConfiguredClientSpec) error {
	if !spec.ExchangeRefreshTokens {
		return nil
	}
	usesKey := spec.TokenEndpointAuthMethod == oauthproto.TokenEndpointAuthMethodPrivateKeyJWT
	if !usesKey && !spec.HasSecret {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens requires a confidential client")
	}
	if !slices.Contains(effectiveConfiguredGrantTypes(spec.GrantTypes), oauthproto.GrantTypeTokenExchange) {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens requires the %q grant", oauthproto.GrantTypeTokenExchange)
	}
	if spec.SelfIssuedSubjectTokens != selfIssuedSubjectTokensRestricted {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens requires self_issued_subject_tokens %q",
			selfIssuedSubjectTokensRestricted)
	}
	if spec.APIAudience == "" {
		return fmt.Errorf("token_exchange.api_audience is required with token_exchange.exchange_refresh_tokens")
	}
	if spec.IDJAGMaxLifetime == "" && spec.RefreshTokenMaxLifetime == "" {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens needs id_jag_max_lifetime or refresh_token_max_lifetime")
	}
	if spec.IDJAGMaxLifetime != "" {
		if err := validateExchangeRefreshTokenLifetime("id_jag_max_lifetime", spec.IDJAGMaxLifetime); err != nil {
			return err
		}
	}
	if spec.RefreshTokenMaxLifetime != "" {
		if err := validateExchangeRefreshTokenLifetime("refresh_token_max_lifetime", spec.RefreshTokenMaxLifetime); err != nil {
			return err
		}
	}
	return nil
}

// validateExchangeRefreshTokenLifetime checks one lifetime cap: a Go duration
// string that is positive and at most maxExchangeRefreshTokenLifetime.
func validateExchangeRefreshTokenLifetime(field, value string) error {
	d, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens.%s: %w", field, err)
	}
	if d <= 0 {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens.%s must be positive, got %q", field, value)
	}
	if d > maxExchangeRefreshTokenLifetime {
		return fmt.Errorf("token_exchange.exchange_refresh_tokens.%s must not exceed %v, got %q",
			field, maxExchangeRefreshTokenLifetime, value)
	}
	return nil
}
