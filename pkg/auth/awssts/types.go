// Package awssts provides AWS STS token exchange with SigV4 signing support.
package awssts

import "time"

// DefaultService is the default service name for AWS MCP Server SigV4 signing.
const DefaultService = "aws-mcp"

// DefaultSessionDuration is the default STS session duration in seconds.
const DefaultSessionDuration int32 = 3600

// MinSessionDuration is the minimum allowed session duration (AWS limit).
const MinSessionDuration int32 = 900

// MaxSessionDuration is the maximum allowed session duration (12 hours).
const MaxSessionDuration int32 = 43200

// DefaultRoleClaim is the default JWT claim to use for role mapping.
const DefaultRoleClaim = "groups"

// Config holds configuration for AWS STS token exchange.
type Config struct {
	// Region is the AWS region for STS and SigV4 signing.
	Region string `json:"region" yaml:"region"`

	// Service is the AWS service name for SigV4 signing (default: "aws-mcp").
	Service string `json:"service" yaml:"service"`

	// RoleArn is the default/fallback IAM role ARN to assume.
	RoleArn string `json:"role_arn,omitempty" yaml:"role_arn,omitempty"`

	// RoleMappings maps JWT claim values to IAM roles with priority.
	RoleMappings []RoleMapping `json:"role_mappings,omitempty" yaml:"role_mappings,omitempty"`

	// RoleClaim is the JWT claim to use for role mapping (default: "groups").
	RoleClaim string `json:"role_claim,omitempty" yaml:"role_claim,omitempty"`

	// SessionDuration is the duration in seconds for assumed role credentials.
	SessionDuration int32 `json:"session_duration,omitempty" yaml:"session_duration,omitempty"`

	// SessionNameClaim is the JWT claim to use for role session name (default: "sub").
	SessionNameClaim string `json:"session_name_claim,omitempty" yaml:"session_name_claim,omitempty"`
}

// RoleMapping maps a JWT claim value to an IAM role with explicit priority.
type RoleMapping struct {
	// Claim is the claim value to match (e.g., group name).
	Claim string `json:"claim" yaml:"claim"`

	// RoleArn is the IAM role ARN to assume when this claim matches.
	RoleArn string `json:"role_arn" yaml:"role_arn"`

	// Priority determines selection order (lower number = higher priority).
	Priority int `json:"priority" yaml:"priority"`
}

// Credentials holds temporary AWS credentials from STS.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	Expiration      time.Time
}

// IsExpired returns true if the credentials have expired.
func (c *Credentials) IsExpired() bool {
	return time.Now().After(c.Expiration)
}

// ShouldRefresh returns true if credentials should be refreshed (5 min buffer).
func (c *Credentials) ShouldRefresh() bool {
	return time.Now().After(c.Expiration.Add(-5 * time.Minute))
}

// GetService returns the configured service or the default.
func (c *Config) GetService() string {
	if c.Service != "" {
		return c.Service
	}
	return DefaultService
}

// GetRoleClaim returns the configured role claim or the default.
func (c *Config) GetRoleClaim() string {
	if c.RoleClaim != "" {
		return c.RoleClaim
	}
	return DefaultRoleClaim
}

// GetSessionDuration returns the configured session duration or the default.
// The returned value is clamped to AWS limits (900-43200 seconds).
func (c *Config) GetSessionDuration() int32 {
	if c.SessionDuration > 0 {
		if c.SessionDuration < MinSessionDuration {
			return MinSessionDuration
		}
		if c.SessionDuration > MaxSessionDuration {
			return MaxSessionDuration
		}
		return c.SessionDuration
	}
	return DefaultSessionDuration
}
