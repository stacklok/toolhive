// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import "errors"

// Sentinel errors for AWS STS operations.
var (
	// ErrNoRoleMapping is returned when no role mapping matches the JWT claims.
	ErrNoRoleMapping = errors.New("no role mapping found for JWT claims")

	// ErrInvalidRoleArn is returned when the role ARN format is invalid.
	ErrInvalidRoleArn = errors.New("invalid IAM role ARN format")

	// ErrMissingRegion is returned when region is not configured.
	ErrMissingRegion = errors.New("AWS region is required")

	// ErrMissingRoleConfig is returned when neither role_arn nor role_mappings is configured.
	ErrMissingRoleConfig = errors.New("either role_arn or role_mappings must be configured")

	// ErrInvalidRoleMapping is returned when a role mapping has invalid configuration.
	ErrInvalidRoleMapping = errors.New("invalid role mapping configuration")

	// ErrInvalidMatcher is returned when a CEL matcher expression is invalid.
	ErrInvalidMatcher = errors.New("invalid CEL matcher expression")
)
