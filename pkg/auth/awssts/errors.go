package awssts

import "errors"

// Sentinel errors for AWS STS operations.
var (
	// ErrNoRoleMapping is returned when no role mapping matches the JWT claims.
	ErrNoRoleMapping = errors.New("no role mapping found for JWT claims")

	// ErrInvalidToken is returned when the JWT token is invalid or expired.
	ErrInvalidToken = errors.New("invalid or expired identity token")

	// ErrAccessDenied is returned when the trust policy doesn't allow the assumption.
	ErrAccessDenied = errors.New("access denied - trust policy does not allow role assumption")

	// ErrSTSUnavailable is returned when the STS service is unavailable.
	ErrSTSUnavailable = errors.New("AWS STS service unavailable")

	// ErrInvalidRoleArn is returned when the role ARN format is invalid.
	ErrInvalidRoleArn = errors.New("invalid IAM role ARN format")

	// ErrMissingRegion is returned when region is not configured.
	ErrMissingRegion = errors.New("AWS region is required")

	// ErrMissingRoleConfig is returned when neither role_arn nor role_mappings is configured.
	ErrMissingRoleConfig = errors.New("either role_arn or role_mappings must be configured")
)
