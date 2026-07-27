// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment-driven configuration (the sidecar is configured exclusively
// via env, injected at clone time — no flags, no config files the backend
// could influence).
const (
	// EnvPolicyFile is the mounted policy ConfigMap file path.
	EnvPolicyFile = "THV_EGRESSBROKER_POLICY_FILE"
	// EnvCAFile is the bump-CA public cert path (from the CA emptyDir).
	EnvCAFile = "THV_EGRESSBROKER_CA_FILE"
	// EnvCAKeyFile is the bump-CA private key path (sidecar-only Secret mount).
	EnvCAKeyFile = "THV_EGRESSBROKER_CA_KEY_FILE"
	// EnvListenAddress is the ext_authz gRPC listen address (default 127.0.0.1).
	EnvListenAddress = "THV_EGRESSBROKER_LISTEN_ADDRESS"
	// EnvListenPort is the ext_authz gRPC listen port (default 9001).
	EnvListenPort = "THV_EGRESSBROKER_LISTEN_PORT"
	// EnvDialAllowlist is a comma-separated CIDR/IP list for the D7 per-dial
	// resolved-IP validation.
	EnvDialAllowlist = "THV_EGRESSBROKER_DIAL_ALLOWLIST"
	// EnvScanFailClosed flips the response scanner (D6c) off its DOCUMENTED
	// fail-open default: when "true", a scanner-internal error suppresses the
	// response (502) instead of passing it. Governs scanner errors only — a
	// detected credential echo ALWAYS blocks in both modes.
	EnvScanFailClosed = "THV_EGRESSBROKER_SCAN_FAIL_CLOSED"
	// EnvScanMaxBodyBytes overrides the buffered-body scan cap (bytes).
	EnvScanMaxBodyBytes = "THV_EGRESSBROKER_SCAN_MAX_BODY_BYTES"
)

// Defaults for the gRPC listener, the Envoy explicit-proxy port, and the
// response scanner (D6c).
const (
	defaultListenAddress = "127.0.0.1"
	defaultListenPort    = 9001
	// DefaultProxyPort is the Envoy explicit-proxy port the backend's
	// HTTP(S)_PROXY env points at (must match the clone-time wiring).
	DefaultProxyPort = 15001

	// DefaultScanMaxBodyBytes is the buffered-body scan cap (1 MiB). The
	// rendered Envoy ext_proc config buffers at most this much per response.
	DefaultScanMaxBodyBytes = 1 << 20
	// ScanCorrelationTTL bounds how long an injection's scan record lives
	// (request/response round trip plus slack).
	ScanCorrelationTTL = 60 * time.Second
	// ScanCorrelationMaxEntries bounds the request-id → token map (10k).
	ScanCorrelationMaxEntries = 10000
)

// Config is the validated runtime configuration of the broker process.
type Config struct {
	// PolicyFile is the mounted policy ConfigMap file (required).
	PolicyFile string
	// CAFile is the bump-CA cert path (required).
	CAFile string
	// CAKeyFile is the bump-CA private key path (required).
	CAKeyFile string
	// ListenAddress for the ext_authz gRPC server.
	ListenAddress string
	// ListenPort for the ext_authz gRPC server.
	ListenPort int
	// DialAllowlist is the D7 per-dial IP allowlist (required, non-empty).
	DialAllowlist []string
	// ScanFailClosed suppresses responses on scanner-internal errors
	// (default false — the documented fail-open posture, ADR D6c).
	ScanFailClosed bool
	// ScanMaxBodyBytes is the buffered-body scan cap (default 1 MiB).
	ScanMaxBodyBytes int64
}

// LoadConfig reads and validates the process environment. Fails loudly on
// any missing/invalid value — a misconfigured broker must not start (fail
// closed).
func LoadConfig(getenv func(string) string) (*Config, error) {
	if getenv == nil {
		return nil, fmt.Errorf("egressbroker: env lookup must not be nil")
	}
	cfg := &Config{
		PolicyFile:    strings.TrimSpace(getenv(EnvPolicyFile)),
		CAFile:        strings.TrimSpace(getenv(EnvCAFile)),
		CAKeyFile:     strings.TrimSpace(getenv(EnvCAKeyFile)),
		ListenAddress: strings.TrimSpace(getenv(EnvListenAddress)),
		DialAllowlist: splitTrim(getenv(EnvDialAllowlist)),
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = defaultListenAddress
	}
	var err error
	if cfg.ListenPort, err = parsePort(getenv(EnvListenPort)); err != nil {
		return nil, err
	}
	if err := loadScanConfig(cfg, getenv); err != nil {
		return nil, err
	}
	if cfg.PolicyFile == "" {
		return nil, fmt.Errorf("egressbroker: %s must be set (policy ConfigMap mount)", EnvPolicyFile)
	}
	if cfg.CAFile == "" {
		return nil, fmt.Errorf("egressbroker: %s must be set (bump CA cert)", EnvCAFile)
	}
	if cfg.CAKeyFile == "" {
		return nil, fmt.Errorf("egressbroker: %s must be set (bump CA key, sidecar-only mount)", EnvCAKeyFile)
	}
	if len(cfg.DialAllowlist) > 0 {
		// Operator-rendered policies carry the allowlist in the policy file
		// (single source with the NetworkPolicy ipBlocks); the env var is an
		// override for hand-rolled deployments. Validate eagerly either way.
		if _, err := ParseIPAllowlist(cfg.DialAllowlist); err != nil {
			return nil, err
		}
	}
	if _, err := NewPodIdentityResolver(getenv); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ResolveDialAllowlist returns the effective D7 dial allowlist: the env
// override when set, otherwise the operator-rendered policy document's list.
// An empty effective list is a startup error (fail closed).
func (c *Config) ResolveDialAllowlist(policy *EgressPolicy) ([]string, error) {
	if len(c.DialAllowlist) > 0 {
		return c.DialAllowlist, nil
	}
	if policy == nil || len(policy.DialAllowlist()) == 0 {
		return nil, fmt.Errorf("egressbroker: no D7 dial allowlist: %s is unset and the policy document "+
			"carries no dialAllowlist (operator-rendered policy required)", EnvDialAllowlist)
	}
	return policy.DialAllowlist(), nil
}

// ReadPolicyFile loads and compiles the mounted policy document.
func (c *Config) ReadPolicyFile() (*EgressPolicy, error) {
	data, err := os.ReadFile(c.PolicyFile)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to read policy file: %w", err)
	}
	return ParsePolicy(data)
}

// ReadBumpCA loads the bump CA from the mounted cert + key files.
func (c *Config) ReadBumpCA() (*BumpCA, error) {
	certPEM, err := os.ReadFile(c.CAFile)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to read bump CA cert: %w", err)
	}
	keyPEM, err := os.ReadFile(c.CAKeyFile)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to read bump CA key: %w", err)
	}
	return ParseBumpCA(certPEM, keyPEM)
}

// parsePort reads an optional port env value (default when empty).
func parsePort(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultListenPort, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("egressbroker: %s value %q is not a valid port", EnvListenPort, raw)
	}
	return port, nil
}

// loadScanConfig reads the D6c scanner tunables (fail-closed opt-in, body
// cap) with their documented defaults.
func loadScanConfig(cfg *Config, getenv func(string) string) error {
	if raw := strings.TrimSpace(getenv(EnvScanFailClosed)); raw != "" {
		failClosed, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("egressbroker: %s value %q is not a boolean", EnvScanFailClosed, raw)
		}
		cfg.ScanFailClosed = failClosed
	}
	cfg.ScanMaxBodyBytes = DefaultScanMaxBodyBytes
	if raw := strings.TrimSpace(getenv(EnvScanMaxBodyBytes)); raw != "" {
		maxBytes, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || maxBytes <= 0 {
			return fmt.Errorf("egressbroker: %s value %q is not a positive byte count", EnvScanMaxBodyBytes, raw)
		}
		cfg.ScanMaxBodyBytes = maxBytes
	}
	return nil
}

func splitTrim(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
