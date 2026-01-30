// Copyright 2025 Stacklok, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package storage

import "time"

// Type defines the type of storage backend.
type Type string

const (
	// TypeMemory uses in-memory storage (default).
	TypeMemory Type = "memory"

	// DefaultCleanupInterval is how often the background cleanup runs.
	DefaultCleanupInterval = 5 * time.Minute

	// DefaultAccessTokenTTL is the default TTL for access tokens when not extractable from session.
	DefaultAccessTokenTTL = 1 * time.Hour

	// DefaultRefreshTokenTTL is the default TTL for refresh tokens when not extractable from session.
	DefaultRefreshTokenTTL = 30 * 24 * time.Hour // 30 days

	// DefaultAuthCodeTTL is the default TTL for authorization codes (RFC 6749 recommendation).
	DefaultAuthCodeTTL = 10 * time.Minute

	// DefaultInvalidatedCodeTTL is how long invalidated codes are kept for replay detection.
	DefaultInvalidatedCodeTTL = 30 * time.Minute

	// DefaultPKCETTL is the default TTL for PKCE requests (same as auth codes).
	DefaultPKCETTL = 10 * time.Minute
)

// Config configures the storage backend.
type Config struct {
	// Type specifies the storage backend type. Defaults to memory.
	Type Type
}

// DefaultConfig returns sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		Type: TypeMemory,
	}
}

// RunConfig is the serializable storage configuration for RunConfig.
// This is used when the config needs to be passed across process boundaries
// (e.g., in Kubernetes operator).
type RunConfig struct {
	// Type specifies the storage backend type. Defaults to "memory".
	Type string `json:"type,omitempty" yaml:"type,omitempty"`
}
