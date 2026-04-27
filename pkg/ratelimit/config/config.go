// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package config defines runtime-neutral rate limiting configuration.
package config

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Config defines rate limiting configuration for an MCP server.
// +gendoc
type Config struct {
	// Global is a token bucket shared across all users for the entire server.
	Global *Bucket `json:"global,omitempty" yaml:"global,omitempty"`

	// Shared is a deprecated alias for Global. It is kept for compatibility with
	// older CRDs and config files that used "shared" before the RFC settled on
	// "global".
	Shared *Bucket `json:"shared,omitempty" yaml:"shared,omitempty"`

	// PerUser is a token bucket applied independently to each authenticated user
	// at the server level.
	PerUser *Bucket `json:"perUser,omitempty" yaml:"perUser,omitempty"`

	// Tools defines per-tool rate limits.
	Tools []ToolConfig `json:"tools,omitempty" yaml:"tools,omitempty"`
}

// EffectiveGlobal returns the configured server-level global bucket, including
// the deprecated shared alias.
func (c *Config) EffectiveGlobal() *Bucket {
	if c == nil {
		return nil
	}
	if c.Global != nil {
		return c.Global
	}
	return c.Shared
}

// Bucket defines a token bucket configuration with a maximum capacity and a
// refill period.
// +gendoc
type Bucket struct {
	// MaxTokens is the maximum number of tokens.
	MaxTokens int32 `json:"maxTokens" yaml:"maxTokens"`

	// RefillPeriod is the duration to fully refill the bucket.
	RefillPeriod metav1.Duration `json:"refillPeriod" yaml:"refillPeriod"`
}

// ToolConfig defines rate limits for a specific tool.
// +gendoc
type ToolConfig struct {
	// Name is the MCP tool name this limit applies to.
	Name string `json:"name" yaml:"name"`

	// Global is a token bucket shared across all users for this tool.
	Global *Bucket `json:"global,omitempty" yaml:"global,omitempty"`

	// Shared is a deprecated alias for Global.
	Shared *Bucket `json:"shared,omitempty" yaml:"shared,omitempty"`

	// PerUser is a token bucket applied independently to each authenticated user
	// for this tool.
	PerUser *Bucket `json:"perUser,omitempty" yaml:"perUser,omitempty"`
}

// EffectiveGlobal returns the configured tool-level global bucket, including
// the deprecated shared alias.
func (c *ToolConfig) EffectiveGlobal() *Bucket {
	if c == nil {
		return nil
	}
	if c.Global != nil {
		return c.Global
	}
	return c.Shared
}

// DeepCopyInto copies the receiver into out.
func (in *Config) DeepCopyInto(out *Config) {
	*out = *in
	if in.Global != nil {
		out.Global = in.Global.DeepCopy()
	}
	if in.Shared != nil {
		out.Shared = in.Shared.DeepCopy()
	}
	if in.PerUser != nil {
		out.PerUser = in.PerUser.DeepCopy()
	}
	if in.Tools != nil {
		out.Tools = make([]ToolConfig, len(in.Tools))
		for i := range in.Tools {
			in.Tools[i].DeepCopyInto(&out.Tools[i])
		}
	}
}

// DeepCopy returns a copy of the receiver.
func (in *Config) DeepCopy() *Config {
	if in == nil {
		return nil
	}
	out := new(Config)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *Bucket) DeepCopyInto(out *Bucket) {
	*out = *in
}

// DeepCopy returns a copy of the receiver.
func (in *Bucket) DeepCopy() *Bucket {
	if in == nil {
		return nil
	}
	out := new(Bucket)
	in.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (in *ToolConfig) DeepCopyInto(out *ToolConfig) {
	*out = *in
	if in.Global != nil {
		out.Global = in.Global.DeepCopy()
	}
	if in.Shared != nil {
		out.Shared = in.Shared.DeepCopy()
	}
	if in.PerUser != nil {
		out.PerUser = in.PerUser.DeepCopy()
	}
}

// DeepCopy returns a copy of the receiver.
func (in *ToolConfig) DeepCopy() *ToolConfig {
	if in == nil {
		return nil
	}
	out := new(ToolConfig)
	in.DeepCopyInto(out)
	return out
}
