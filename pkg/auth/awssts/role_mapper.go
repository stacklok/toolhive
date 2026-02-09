// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws/arn"
	celgo "github.com/google/cel-go/cel"

	"github.com/stacklok/toolhive-core/cel"
	"github.com/stacklok/toolhive/pkg/logger"
)

// claimBindingExpression is the generic CEL expression used for claim-based role mappings.
// Instead of interpolating user-supplied claim values into CEL expression strings,
// we bind them as variables at evaluation time — making CEL injection impossible by design.
const claimBindingExpression = `claim_value in claims[role_claim_key]`

// newMatcherEngine creates a CEL engine for admin-authored matcher expressions.
// The only available variable is "claims" as a map[string]any.
func newMatcherEngine() *cel.Engine {
	return cel.NewEngine(
		celgo.Variable("claims", celgo.MapType(celgo.StringType, celgo.DynType)),
	)
}

// newClaimBindingEngine creates a CEL engine for claim-based mappings that uses
// variable binding instead of string interpolation. Three variables are available:
//   - claims: the JWT claims map
//   - claim_value: the claim value to match (e.g. "admins")
//   - role_claim_key: the claims map key to look up (e.g. "groups")
func newClaimBindingEngine() *cel.Engine {
	return cel.NewEngine(
		celgo.Variable("claims", celgo.MapType(celgo.StringType, celgo.DynType)),
		celgo.Variable("claim_value", celgo.StringType),
		celgo.Variable("role_claim_key", celgo.StringType),
	)
}

// ValidateRoleArn validates that the given string is a valid IAM role ARN.
// It accepts ARNs from all AWS partitions (aws, aws-cn, aws-us-gov) and
// supports role paths (e.g., arn:aws:iam::123456789012:role/service-role/MyRole).
func ValidateRoleArn(roleArn string) error {
	if roleArn == "" {
		return fmt.Errorf("%w: ARN is empty", ErrInvalidRoleArn)
	}

	// Use AWS SDK to parse the ARN
	parsed, err := arn.Parse(roleArn)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidRoleArn, roleArn)
	}

	// Verify it's an IAM role
	if parsed.Service != "iam" {
		return fmt.Errorf("%w: not an IAM ARN: %s", ErrInvalidRoleArn, roleArn)
	}

	// Resource should start with "role/"
	if !strings.HasPrefix(parsed.Resource, "role/") {
		return fmt.Errorf("%w: not a role ARN: %s", ErrInvalidRoleArn, roleArn)
	}

	// Verify account ID is present and valid (12 digits)
	if len(parsed.AccountID) != 12 {
		return fmt.Errorf("%w: invalid account ID: %s", ErrInvalidRoleArn, roleArn)
	}

	return nil
}

// compiledMapping holds a role mapping with its compiled CEL expression.
type compiledMapping struct {
	roleArn    string
	priority   int
	expr       *cel.CompiledExpression
	claimValue string // non-empty for claim-based mappings; empty for matcher-based
}

// evalContext builds the CEL variable bindings for evaluating this mapping.
// Claim-based mappings bind claim_value and role_claim_key as variables so that
// user-supplied values are never interpolated into CEL expression strings,
// eliminating CEL injection by design. Matcher-based mappings only need claims.
func (cm *compiledMapping) evalContext(claims map[string]any, roleClaim string) map[string]any {
	if cm.claimValue != "" {
		return map[string]any{
			"claims":         claims,
			"claim_value":    cm.claimValue,
			"role_claim_key": roleClaim,
		}
	}
	return map[string]any{"claims": claims}
}

// RoleMapper handles mapping JWT claims to IAM roles with priority-based selection.
// It uses CEL expressions for flexible claim matching.
type RoleMapper struct {
	config   *Config
	mappings []compiledMapping
}

// NewRoleMapper creates a new RoleMapper with the provided configuration.
// It validates the configuration and compiles all CEL expressions during construction.
// Returns an error if the configuration is invalid or any expression fails to compile.
//
// ValidateConfig is called internally, so callers do not need to call both.
func NewRoleMapper(cfg *Config) (*RoleMapper, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	claimEngine := newClaimBindingEngine()
	matcherEngine := newMatcherEngine()

	claimExpr, err := claimEngine.Compile(claimBindingExpression)
	if err != nil {
		return nil, fmt.Errorf("compiling claim binding expression: %w", err)
	}

	rm := &RoleMapper{
		config:   cfg,
		mappings: make([]compiledMapping, 0, len(cfg.RoleMappings)),
	}

	for i, mapping := range cfg.RoleMappings {
		if mapping.Claim != "" {
			rm.mappings = append(rm.mappings, compiledMapping{
				roleArn:    mapping.RoleArn,
				priority:   mapping.Priority,
				expr:       claimExpr,
				claimValue: mapping.Claim,
			})
			continue
		}

		expr, err := matcherEngine.Compile(mapping.Matcher)
		if err != nil {
			return nil, fmt.Errorf("role mapping at index %d: %w: %w", i, ErrInvalidMatcher, err)
		}
		rm.mappings = append(rm.mappings, compiledMapping{
			roleArn:  mapping.RoleArn,
			priority: mapping.Priority,
			expr:     expr,
		})
	}

	return rm, nil
}

// SelectRole selects the appropriate IAM role based on JWT claims.
// It returns the role ARN to assume based on the following logic:
//  1. If no role mappings are configured, return the FallbackRoleArn
//  2. Evaluate each mapping's CEL expression against the claims
//  3. Collect all matching mappings
//  4. Sort matches by priority (lower number = higher priority)
//  5. Return the highest priority match
//  6. If no matches found, fall back to the FallbackRoleArn
func (rm *RoleMapper) SelectRole(claims map[string]any) (string, error) {
	// If no role mappings configured, use default role
	if len(rm.mappings) == 0 {
		if rm.config.FallbackRoleArn == "" {
			return "", ErrMissingRoleConfig
		}
		return rm.config.FallbackRoleArn, nil
	}

	// Find all matching mappings
	roleClaim := rm.config.GetRoleClaim()

	var matches []compiledMapping
	for _, mapping := range rm.mappings {
		match, err := mapping.expr.EvaluateBool(mapping.evalContext(claims, roleClaim))
		if err != nil {
			logger.Debugw("CEL expression evaluation failed, skipping mapping",
				"role_arn", mapping.roleArn, "error", err)
			continue
		}

		if match {
			matches = append(matches, mapping)
		}
	}

	// If no matches, fall back to default role
	if len(matches) == 0 {
		if rm.config.FallbackRoleArn == "" {
			return "", fmt.Errorf("%w: no mapping matched for the provided claims", ErrNoRoleMapping)
		}
		return rm.config.FallbackRoleArn, nil
	}

	// Sort by priority (lower number = higher priority).
	// SortStableFunc preserves configuration order as a tie-breaker
	// when priorities are equal.
	slices.SortStableFunc(matches, func(a, b compiledMapping) int {
		return cmp.Compare(a.priority, b.priority)
	})

	// Return the highest priority match (lowest priority number)
	return matches[0].roleArn, nil
}

// ValidateConfig validates the AWS STS configuration structure.
// It checks that required fields are present, ARNs are well-formed,
// and session duration is within bounds.
//
// This performs structural validation only — CEL expression compilation is handled
// by NewRoleMapper. It is safe to call standalone for early validation at config
// load time. NewRoleMapper calls this internally, so callers do not need to call both.
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// Region is required
	if cfg.Region == "" {
		return ErrMissingRegion
	}

	// Either FallbackRoleArn or RoleMappings must be configured
	if cfg.FallbackRoleArn == "" && len(cfg.RoleMappings) == 0 {
		return ErrMissingRoleConfig
	}

	// Validate FallbackRoleArn if provided
	if cfg.FallbackRoleArn != "" {
		if err := ValidateRoleArn(cfg.FallbackRoleArn); err != nil {
			return err
		}
	}

	// Validate all role mappings (structural checks only)
	for i, mapping := range cfg.RoleMappings {
		if err := validateRoleMapping(i, mapping); err != nil {
			return err
		}
	}

	// Validate session duration if specified
	if cfg.SessionDuration != 0 {
		if cfg.SessionDuration < MinSessionDuration {
			return fmt.Errorf("session duration %d is below minimum %d seconds", cfg.SessionDuration, MinSessionDuration)
		}
		if cfg.SessionDuration > MaxSessionDuration {
			return fmt.Errorf("session duration %d exceeds maximum %d seconds", cfg.SessionDuration, MaxSessionDuration)
		}
	}

	return nil
}

// validateRoleMapping validates the structural properties of a single role mapping.
func validateRoleMapping(index int, mapping RoleMapping) error {
	// Exactly one of Claim or Matcher must be set
	if mapping.Claim == "" && mapping.Matcher == "" {
		return fmt.Errorf("%w at index %d: either claim or matcher must be set", ErrInvalidRoleMapping, index)
	}
	if mapping.Claim != "" && mapping.Matcher != "" {
		return fmt.Errorf("%w at index %d: claim and matcher are mutually exclusive", ErrInvalidRoleMapping, index)
	}

	// RoleArn is required
	if mapping.RoleArn == "" {
		return fmt.Errorf("role mapping at index %d has empty role ARN", index)
	}

	// Validate the role ARN
	if err := ValidateRoleArn(mapping.RoleArn); err != nil {
		return fmt.Errorf("role mapping at index %d: %w", index, err)
	}

	return nil
}
