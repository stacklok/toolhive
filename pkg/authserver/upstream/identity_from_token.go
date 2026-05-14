// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstream

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tidwall/gjson"
)

// partialIdentity holds identity fields extracted from a token response body.
// It is used internally to pass the extracted subject, name, and email
// between extractIdentityFromTokenResponse and the provider layer.
type partialIdentity struct {
	Subject string
	Name    string
	Email   string
}

// IdentityFromTokenConfig is the runtime configuration for extracting user
// identity directly from an OAuth2 token endpoint response body.
//
// Each path is a gjson dot-notation path (e.g. "username" or
// "associated_user.id") into the raw JSON body returned by the token
// endpoint. Path semantics, trust-model warnings, and uniqueness notes are
// documented on the corresponding CRD type
// (cmd/thv-operator/api/v1alpha1.IdentityFromTokenConfig).
type IdentityFromTokenConfig struct {
	// SubjectPath is the gjson path to the unique user identifier (required).
	SubjectPath string

	// NamePath is the gjson path to the user's display name (optional).
	// Leave empty to skip name extraction.
	NamePath string

	// EmailPath is the gjson path to the user's email address (optional).
	// Leave empty to skip email extraction.
	EmailPath string
}

// RegisterModifiers registers the gjson custom modifiers used by this
// package's path-based identity extractors. Call once during application
// or test wire-up before invoking any extractor that consumes a
// modifier-bearing path. Repeated calls are safe — gjson.AddModifier
// overwrites the existing entry.
//
// Modifiers registered:
//   - @upstreamjwt: see upstreamJWTModifier.
func RegisterModifiers() {
	gjson.AddModifier("upstreamjwt", upstreamJWTModifier)
}

// extractIdentityFromTokenResponse extracts user identity fields from a raw
// OAuth2 token endpoint response body using the paths in cfg.
//
// SubjectPath must resolve to a string or number value; objects, arrays, null,
// and missing paths are rejected with ErrIdentityResolutionFailed. NamePath
// and EmailPath are optional: type mismatches or missing paths produce a
// slog.Warn and leave the respective field empty. Empty NamePath/EmailPath in
// cfg means "do not extract" and are skipped silently.
func extractIdentityFromTokenResponse(body []byte, cfg *IdentityFromTokenConfig) (partialIdentity, error) {
	if cfg == nil {
		return partialIdentity{}, errors.New("identity extraction config is required")
	}

	subjectResult := gjson.GetBytes(body, cfg.SubjectPath)
	if err := validateIdentityField(subjectResult); err != nil {
		return partialIdentity{}, fmt.Errorf("%w: subjectPath %q %s", ErrIdentityResolutionFailed, cfg.SubjectPath, err.Error())
	}

	name := extractOptionalField(body, cfg.NamePath, "namePath")
	email := extractOptionalField(body, cfg.EmailPath, "emailPath")

	return partialIdentity{
		Subject: scalarToString(subjectResult),
		Name:    name,
		Email:   email,
	}, nil
}

// scalarToString returns the string representation of a gjson scalar value.
// For Number, it returns the raw JSON token rather than gjson.Result.String(),
// which formats via float64 and would lose precision for integer IDs larger
// than 2^53 (e.g., some upstream providers return 64-bit numeric subjects).
// For String, gjson.Result.String() correctly strips the surrounding quotes.
// The caller must already have validated the type.
func scalarToString(r gjson.Result) string {
	if r.Type == gjson.Number {
		return r.Raw
	}
	return r.String()
}

// validateIdentityField checks that a gjson result is a non-empty scalar
// (string or number). Returns a descriptive error on failure.
func validateIdentityField(result gjson.Result) error {
	if !result.Exists() {
		return errors.New("path not found in token response")
	}
	switch result.Type {
	case gjson.String:
		if result.String() == "" {
			return errors.New("resolved to an empty string")
		}
		return nil
	case gjson.Number:
		return nil
	case gjson.JSON:
		return errors.New("resolved to an object or array, expected a scalar")
	case gjson.Null, gjson.False, gjson.True:
		return errors.New("resolved to null or unsupported type")
	}
	// Unreachable: all gjson.Type cases are handled above.
	return errors.New("unrecognised gjson result type")
}

// extractOptionalField extracts an optional identity field from the token body.
// Returns an empty string if the path is not configured, missing, or has an
// unexpected type (with a slog.Warn for unexpected types).
func extractOptionalField(body []byte, path, fieldName string) string {
	if path == "" {
		return ""
	}
	result := gjson.GetBytes(body, path)
	if !result.Exists() {
		slog.Warn("optional identity field not found in token response", "field", fieldName, "path", path)
		return ""
	}
	switch result.Type {
	case gjson.String, gjson.Number:
		return scalarToString(result)
	case gjson.JSON, gjson.Null, gjson.False, gjson.True:
		slog.Warn("optional identity field has unexpected type, skipping", "field", fieldName, "path", path)
		return ""
	}
	// Unreachable: all gjson.Type cases are handled above.
	return ""
}

// upstreamJWTModifier is a gjson modifier that decodes the payload of a
// JWT-shaped string value and returns it as JSON, enabling further gjson
// path drilling (e.g. "access_token|@upstreamjwt|sub").
//
// Trust model: NO signature verification. Use only for JWTs received over
// a TLS-authenticated channel directly from the upstream AS's token
// endpoint, where the channel itself provides integrity. For JWTs that
// have transited an untrusted hop, configure the upstream as OIDC and
// use the existing signed-ID-token path instead.
//
// Failure modes (all return ""):
//   - input is not a JSON string
//   - input does not contain exactly three dot-separated parts
//   - the second part is not valid base64url
//
// Returning "" causes the next pipe stage to resolve to gjson.Null, which
// the caller's validateIdentityField rejects as "path not found".
func upstreamJWTModifier(jsonValue, _ string) string {
	token := gjson.Parse(jsonValue).String()
	if token == "" {
		return ""
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	return string(payload)
}
