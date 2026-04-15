// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package oauthparams provides shared definitions for reserved OAuth2
// authorization parameters that are managed by the framework.
package oauthparams

import "fmt"

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
