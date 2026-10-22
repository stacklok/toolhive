// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"
	"slices"

	"github.com/go-jose/go-jose/v3"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// defaultConfiguredSigningAlgorithm is the private_key_jwt signing algorithm
// a configured client uses when none is declared.
const defaultConfiguredSigningAlgorithm = "RS256"

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
	return validateConfiguredRedirects(spec.RedirectURIs, grants)
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
