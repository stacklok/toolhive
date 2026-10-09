// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package redisconfig provides serializable Redis connection settings.
//
// +groupName=toolhive.stacklok.dev
// +versionName=redisconfig
package redisconfig

import (
	"fmt"
	"os"

	"github.com/stacklok/toolhive-core/redisconn"
)

// TLSConfig configures TLS for a Redis connection. Presence enables TLS;
// an empty configuration verifies the server certificate using system roots.
// +kubebuilder:object:generate=true
// +gendoc
type TLSConfig struct {
	// InsecureSkipVerify skips server certificate and hostname verification.
	// +optional
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty" yaml:"insecureSkipVerify,omitempty"`

	// CACertFile is the path to a PEM-encoded CA bundle. When configured, this
	// bundle replaces the system roots used to verify the Redis server.
	// +optional
	CACertFile string `json:"caCertFile,omitempty" yaml:"caCertFile,omitempty"`
}

// Load resolves and validates the CA bundle for a Redis connection. A nil
// receiver disables TLS. A configured CA file must contain valid certificates;
// errors are returned rather than falling back to system roots or plaintext.
func (c *TLSConfig) Load() (*redisconn.TLSConfig, error) {
	if c == nil {
		return nil, nil
	}
	cfg := &redisconn.TLSConfig{InsecureSkipVerify: c.InsecureSkipVerify}
	if c.CACertFile != "" {
		// #nosec G304 -- The CA file path comes from trusted runtime configuration.
		data, err := os.ReadFile(c.CACertFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read Redis CA cert file %q: %w", c.CACertFile, err)
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("redis CA cert file %q is empty", c.CACertFile)
		}
		cfg.CACert = data
	}
	if _, err := redisconn.BuildTLSConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid Redis TLS configuration: %w", err)
	}
	return cfg, nil
}
