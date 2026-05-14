// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package types

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// RateLimitConfig defines rate limiting configuration for an MCP server.
// At least one of shared, perUser, or tools must be configured.
//
// +kubebuilder:validation:XValidation:rule="has(self.shared) || has(self.perUser) || (has(self.tools) && size(self.tools) > 0)",message="at least one of shared, perUser, or tools must be configured"
// +gendoc
//
//nolint:lll // kubebuilder marker exceeds line length
type RateLimitConfig struct {
	// Shared is a token bucket shared across all users for the entire server.
	// +optional
	Shared *RateLimitBucket `json:"shared,omitempty" yaml:"shared,omitempty"`

	// PerUser is a token bucket applied independently to each authenticated user
	// at the server level. Requires authentication to be enabled.
	// Each unique userID creates Redis keys that expire after 2x refillPeriod.
	// Memory formula: unique_users_per_TTL_window * (1 + num_tools_with_per_user_limits) keys.
	// +optional
	PerUser *RateLimitBucket `json:"perUser,omitempty" yaml:"perUser,omitempty"`

	// Tools defines per-tool rate limit overrides.
	// Each entry applies additional rate limits to calls targeting a specific tool name.
	// A request must pass both the server-level limit and the per-tool limit.
	// +listType=map
	// +listMapKey=name
	// +optional
	Tools []ToolRateLimitConfig `json:"tools,omitempty" yaml:"tools,omitempty"`
}

// RateLimitBucket defines a token bucket configuration with a maximum capacity
// and a refill period. Used by both shared and per-user rate limits.
// +gendoc
type RateLimitBucket struct {
	// MaxTokens is the maximum number of tokens (bucket capacity).
	// This is also the burst size: the maximum number of requests that can be served
	// instantaneously before the bucket is depleted.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	MaxTokens int32 `json:"maxTokens" yaml:"maxTokens"`

	// RefillPeriod is the duration to fully refill the bucket from zero to maxTokens.
	// The effective refill rate is maxTokens / refillPeriod tokens per second.
	// Format: Go duration string (e.g., "1m0s", "30s", "1h0m0s").
	// +kubebuilder:validation:Required
	RefillPeriod metav1.Duration `json:"refillPeriod" yaml:"refillPeriod"`
}

// ToolRateLimitConfig defines rate limits for a specific tool.
// At least one of shared or perUser must be configured.
//
// +kubebuilder:validation:XValidation:rule="has(self.shared) || has(self.perUser)",message="at least one of shared or perUser must be configured"
// +gendoc
//
//nolint:lll // kubebuilder marker exceeds line length
type ToolRateLimitConfig struct {
	// Name is the MCP tool name this limit applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name" yaml:"name"`

	// Shared token bucket for this specific tool.
	// +optional
	Shared *RateLimitBucket `json:"shared,omitempty" yaml:"shared,omitempty"`

	// PerUser token bucket configuration for this tool.
	// +optional
	PerUser *RateLimitBucket `json:"perUser,omitempty" yaml:"perUser,omitempty"`
}
