// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

//nolint:gosec // G101: False positive - OAuth2 URN identifier, not a credential
const grantTypeJWTBearer = "urn:ietf:params:oauth:grant-type:jwt-bearer"

// entraTokenURLTemplate is the Microsoft Entra v2.0 token endpoint format.
//
//nolint:gosec // G101: False positive - URL template, not a credential
const entraTokenURLTemplate = "https://login.microsoftonline.com/%s/oauth2/v2.0/token"

// validEntraTenantID matches Azure AD tenant IDs: GUIDs or verified domain names.
// GUIDs: 8-4-4-4-12 hex digits. Domains: alphanumeric with dots/hyphens.
var validEntraTenantID = regexp.MustCompile(
	`^([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[a-zA-Z0-9][a-zA-Z0-9.-]*\.[a-zA-Z]{2,})$`,
)

// EntraOBOHandler implements VariantHandler for Microsoft Entra ID
// On-Behalf-Of (OBO) flow. It exchanges an incoming user token for a
// downstream token using the JWT bearer assertion grant type.
//
// This handler is temporary in OSS for validation against real Entra
// tenants. After validation, it moves to the enterprise repository.
//
// Entra OBO documentation:
// https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-on-behalf-of-flow
type EntraOBOHandler struct{}

// ResolveTokenURL derives the Entra v2.0 token endpoint from the tenantId
// parameter in config.RawConfig.Parameters. If TokenURL is already set on
// the config (checked by the caller), this method will not be called.
func (*EntraOBOHandler) ResolveTokenURL(config *ExchangeConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("token exchange: config must not be nil")
	}
	if config.RawConfig == nil {
		return "", fmt.Errorf("token exchange: entra variant requires RawConfig with tenantId parameter")
	}

	tenantID := config.RawConfig.Parameters["tenantId"]
	if tenantID == "" {
		return "", fmt.Errorf("token exchange: entra variant requires non-empty tenantId in RawConfig.Parameters")
	}

	// DNS labels are limited to 253 characters; GUIDs are 36.
	const maxTenantIDLen = 253
	if len(tenantID) > maxTenantIDLen {
		return "", fmt.Errorf("token exchange: tenantId exceeds maximum length of %d characters", maxTenantIDLen)
	}

	if !validEntraTenantID.MatchString(tenantID) {
		return "", fmt.Errorf("token exchange: tenantId %q is not a valid GUID or domain name", tenantID)
	}

	return fmt.Sprintf(entraTokenURLTemplate, tenantID), nil
}

// BuildFormData constructs the url.Values for a Microsoft Entra OBO token
// exchange request. Entra OBO sends client credentials as form parameters
// (not HTTP Basic Auth) alongside the JWT bearer assertion.
//
// SECURITY: The subjectToken and client_secret are bearer credentials and
// MUST NOT appear in error messages or logs.
func (*EntraOBOHandler) BuildFormData(config *ExchangeConfig, subjectToken string) (url.Values, error) {
	if config == nil {
		return nil, fmt.Errorf("token exchange: config must not be nil")
	}
	if subjectToken == "" {
		return nil, fmt.Errorf("token exchange: subject_token is required")
	}
	if config.ClientID == "" {
		return nil, fmt.Errorf("token exchange: client_id is required for entra OBO")
	}
	if config.ClientSecret == "" {
		return nil, fmt.Errorf("token exchange: client_secret is required for entra OBO")
	}

	data := url.Values{}
	data.Set("grant_type", grantTypeJWTBearer)
	data.Set("assertion", subjectToken)
	data.Set("requested_token_use", "on_behalf_of")
	data.Set("client_id", config.ClientID)
	data.Set("client_secret", config.ClientSecret)

	if len(config.Scopes) > 0 {
		data.Set("scope", strings.Join(config.Scopes, " "))
	}

	return data, nil
}

// ValidateResponse performs variant-specific validation on the token exchange
// response. Entra OBO does not return issued_token_type (unlike RFC 8693).
// The shared Token() code already validates access_token and token_type, so
// this method only performs a nil check.
func (*EntraOBOHandler) ValidateResponse(resp *Response) error {
	if resp == nil {
		return fmt.Errorf("token exchange: response must not be nil")
	}
	return nil
}

// ClientAuth returns an empty clientAuthentication because Entra OBO sends
// client credentials as form parameters (client_id and client_secret in the
// POST body), not via HTTP Basic Auth.
func (*EntraOBOHandler) ClientAuth(_ *ExchangeConfig) clientAuthentication {
	return clientAuthentication{}
}

func init() {
	RegisterVariantHandler("entra", &EntraOBOHandler{})
}
