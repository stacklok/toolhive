package awssts

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RoleArnPattern is the regex pattern to validate IAM role ARN format.
// Format: arn:aws:iam::{12-digit-account-id}:role/{role-name}
// Role names can contain alphanumeric characters, plus (+), equals (=), comma (,),
// period (.), at (@), underscore (_), and hyphen (-).
var RoleArnPattern = regexp.MustCompile(`^arn:aws:iam::\d{12}:role/[\w+=,.@-]+$`)

// ValidateRoleArn validates that the given string is a valid IAM role ARN.
func ValidateRoleArn(arn string) error {
	if arn == "" {
		return fmt.Errorf("%w: ARN is empty", ErrInvalidRoleArn)
	}
	if !RoleArnPattern.MatchString(arn) {
		return fmt.Errorf("%w: %s", ErrInvalidRoleArn, arn)
	}
	return nil
}

// RoleMapper handles mapping JWT claims to IAM roles with priority-based selection.
type RoleMapper struct {
	config *Config
}

// NewRoleMapper creates a new RoleMapper with the provided configuration.
func NewRoleMapper(cfg *Config) *RoleMapper {
	return &RoleMapper{
		config: cfg,
	}
}

// SelectRole selects the appropriate IAM role based on JWT claims.
// It returns the role ARN to assume based on the following logic:
//  1. If no role mappings are configured, return the default RoleArn
//  2. Extract the claim value from JWT claims using the configured RoleClaim
//  3. Find all mappings that match the claim value
//  4. Sort matches by priority (lower number = higher priority)
//  5. Return the highest priority match
//  6. If no matches found, fall back to the default RoleArn
func (rm *RoleMapper) SelectRole(claims map[string]interface{}) (string, error) {
	// If no role mappings configured, use default role
	if len(rm.config.RoleMappings) == 0 {
		if rm.config.RoleArn == "" {
			return "", ErrMissingRoleConfig
		}
		return rm.config.RoleArn, nil
	}

	// Get the claim to use for role mapping
	roleClaim := rm.config.GetRoleClaim()
	claimValue, exists := claims[roleClaim]
	if !exists {
		// Claim not present, fall back to default role
		if rm.config.RoleArn == "" {
			return "", fmt.Errorf("%w: claim '%s' not found in token", ErrNoRoleMapping, roleClaim)
		}
		return rm.config.RoleArn, nil
	}

	// Extract claim values (handle both single string and array)
	claimValues := rm.extractClaimValues(claimValue)
	if len(claimValues) == 0 {
		if rm.config.RoleArn == "" {
			return "", fmt.Errorf("%w: claim '%s' has no values", ErrNoRoleMapping, roleClaim)
		}
		return rm.config.RoleArn, nil
	}

	// Find all matching mappings
	var matches []RoleMapping
	for _, mapping := range rm.config.RoleMappings {
		for _, cv := range claimValues {
			if mapping.Claim == cv {
				matches = append(matches, mapping)
				break // Don't add the same mapping twice
			}
		}
	}

	// If no matches, fall back to default role
	if len(matches) == 0 {
		if rm.config.RoleArn == "" {
			return "", fmt.Errorf("%w: no mapping matched for claim '%s' values %v", ErrNoRoleMapping, roleClaim, claimValues)
		}
		return rm.config.RoleArn, nil
	}

	// Sort by priority (lower number = higher priority)
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Priority < matches[j].Priority
	})

	// Return the highest priority match (lowest priority number)
	return matches[0].RoleArn, nil
}

// extractClaimValues extracts string values from a claim.
// Supports both single string values and arrays ([]interface{}).
// For string values, also splits on ":" to support AWS principal tag format
// where multiple values are colon-separated (e.g., "s3-readers:Everyone").
func (*RoleMapper) extractClaimValues(claimValue interface{}) []string {
	switch v := claimValue.(type) {
	case string:
		if v == "" {
			return nil
		}
		// Split on ":" to support AWS principal tag format
		// AWS doesn't allow commas in session tags, so groups are colon-separated
		return strings.Split(v, ":")
	case []interface{}:
		var values []string
		for _, item := range v {
			if str, ok := item.(string); ok && str != "" {
				values = append(values, str)
			}
		}
		return values
	case []string:
		var values []string
		for _, str := range v {
			if str != "" {
				values = append(values, str)
			}
		}
		return values
	default:
		return nil
	}
}

// ValidateConfig validates the AWS STS configuration.
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	// Region is required
	if cfg.Region == "" {
		return ErrMissingRegion
	}

	// Either RoleArn or RoleMappings must be configured
	if cfg.RoleArn == "" && len(cfg.RoleMappings) == 0 {
		return ErrMissingRoleConfig
	}

	// Validate default RoleArn if provided
	if cfg.RoleArn != "" {
		if !RoleArnPattern.MatchString(cfg.RoleArn) {
			return fmt.Errorf("%w: %s", ErrInvalidRoleArn, cfg.RoleArn)
		}
	}

	// Validate all role mappings
	for i, mapping := range cfg.RoleMappings {
		if mapping.Claim == "" {
			return fmt.Errorf("role mapping at index %d has empty claim", i)
		}
		if mapping.RoleArn == "" {
			return fmt.Errorf("role mapping at index %d has empty role ARN", i)
		}
		if !RoleArnPattern.MatchString(mapping.RoleArn) {
			return fmt.Errorf("%w: role mapping at index %d has invalid ARN: %s", ErrInvalidRoleArn, i, mapping.RoleArn)
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
