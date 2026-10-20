// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oauthparams provides shared definitions for reserved OAuth2
// authorization parameters that are managed by the framework.
package oauthparams

import (
	"fmt"
	"net/url"
	"strings"
)

// ReservedAuthorizationParams are OAuth2 parameters managed by the framework
// that must not be set via AdditionalAuthorizationParams.
var ReservedAuthorizationParams = map[string]bool{
	"response_type":         true,
	"client_id":             true,
	"redirect_uri":          true,
	"scope":                 true,
	"state":                 true,
	"code_challenge":        true,
	"code_challenge_method": true,
	"nonce":                 true,
}

// ReservedTokenParams are OAuth2 parameters managed by the framework that
// must not be set via AdditionalTokenParams. They cover both token-endpoint
// grant types the framework issues (authorization_code and refresh_token),
// plus the RFC 7523 client-authentication credentials: letting those through
// would combine an assertion with the configured client_secret or HTTP Basic
// credentials into an invalid multi-method client-auth request, and would put
// a credential in plain configuration instead of a secret-backed path.
var ReservedTokenParams = map[string]bool{
	"grant_type":            true,
	"code":                  true,
	"redirect_uri":          true,
	"client_id":             true,
	"client_secret":         true,
	"code_verifier":         true,
	"refresh_token":         true,
	"scope":                 true,
	"client_assertion":      true,
	"client_assertion_type": true,
}

// Validate checks that no key in params is a reserved OAuth2 authorization
// parameter. Reserved parameters are managed by the framework and cannot be
// overridden via additional authorization params.
func Validate(params map[string]string) error {
	for k := range params {
		if ReservedAuthorizationParams[k] {
			return fmt.Errorf("reserved parameter %q is managed by the framework and cannot be overridden", k)
		}
	}
	return nil
}

// ValidateTokenParams checks that no key in params is a reserved OAuth2
// token-request parameter. Reserved parameters are managed by the framework
// and cannot be overridden via additional token params. A "resource" entry is
// additionally checked against the RFC 8707 shape.
func ValidateTokenParams(params map[string]string) error {
	for k, v := range params {
		if ReservedTokenParams[k] {
			return fmt.Errorf("reserved token parameter %q is managed by the framework and cannot be overridden", k)
		}
		if k == resourceParam {
			if err := validateResourceIndicator(v); err != nil {
				return err
			}
		}
	}
	return nil
}

// resourceParam is the RFC 8707 resource indicator parameter name.
const resourceParam = "resource"

// validateResourceIndicator enforces the RFC 8707 shape for a resource
// indicator: an absolute URI carrying no fragment component. Rejecting it here
// turns an authorization-server error at first login into a config-time error.
func validateResourceIndicator(value string) error {
	if value == "" {
		return fmt.Errorf("token parameter %q must not be empty", resourceParam)
	}
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("token parameter %q must be an absolute URI: %w", resourceParam, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("token parameter %q must be an absolute URI, got %q", resourceParam, value)
	}
	if u.Fragment != "" || strings.Contains(value, "#") {
		return fmt.Errorf("token parameter %q must not contain a fragment, got %q", resourceParam, value)
	}
	return nil
}
