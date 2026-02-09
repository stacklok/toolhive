// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package awssts provides AWS STS token exchange with SigV4 signing support.
package awssts

// MinSessionDuration is the minimum allowed session duration (AWS limit).
const MinSessionDuration int32 = 900

// MaxSessionDuration is the maximum allowed session duration (12 hours).
const MaxSessionDuration int32 = 43200

// defaultRoleClaim is the default JWT claim to use for role mapping.
const defaultRoleClaim = "groups"

// Config holds configuration for AWS STS token exchange.
type Config struct {
	// Region is the AWS region for STS and SigV4 signing.
	Region string `json:"region" yaml:"region"`

	// Service is the AWS service name for SigV4 signing (default: "aws-mcp").
	Service string `json:"service" yaml:"service"`

	// FallbackRoleArn is the IAM role ARN to assume when no role mapping matches.
	FallbackRoleArn string `json:"fallback_role_arn,omitempty" yaml:"fallback_role_arn,omitempty"`

	// RoleMappings maps JWT claim values to IAM roles with priority.
	RoleMappings []RoleMapping `json:"role_mappings,omitempty" yaml:"role_mappings,omitempty"`

	// RoleClaim is the JWT claim to use for role mapping (default: "groups").
	RoleClaim string `json:"role_claim,omitempty" yaml:"role_claim,omitempty"`

	// SessionDuration is the duration in seconds for assumed role credentials.
	SessionDuration int32 `json:"session_duration,omitempty" yaml:"session_duration,omitempty"`

	// SessionNameClaim is the JWT claim to use for role session name (default: "sub").
	SessionNameClaim string `json:"session_name_claim,omitempty" yaml:"session_name_claim,omitempty"`
}

// GetRoleClaim returns the configured role claim or the default.
func (c *Config) GetRoleClaim() string {
	if c.RoleClaim != "" {
		return c.RoleClaim
	}
	return defaultRoleClaim
}

// RoleMapping maps a JWT claim value or CEL expression to an IAM role with explicit priority.
type RoleMapping struct {
	// Claim is the simple claim value to match (e.g., group name).
	// Internally compiles to a CEL expression: "<claim_value>" in claims["<role_claim>"]
	// Mutually exclusive with Matcher.
	Claim string `json:"claim,omitempty" yaml:"claim,omitempty"`

	// Matcher is a CEL expression for complex matching against JWT claims.
	// The expression has access to a "claims" variable containing all JWT claims.
	// Examples:
	//   - "admins" in claims["groups"]
	//   - claims["sub"] == "user123" && !("act" in claims)
	// Mutually exclusive with Claim.
	Matcher string `json:"matcher,omitempty" yaml:"matcher,omitempty"`

	// RoleArn is the IAM role ARN to assume when this mapping matches.
	RoleArn string `json:"role_arn" yaml:"role_arn"`

	// Priority determines selection order (lower number = higher priority).
	// When multiple mappings match, the one with the lowest priority is selected.
	// When nil (omitted), the mapping has the lowest possible priority, and
	// configuration order acts as tie-breaker via stable sort.
	Priority *int `json:"priority,omitempty" yaml:"priority,omitempty"`
}
