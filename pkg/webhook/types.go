// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package webhook implements the core types, HTTP client, HMAC signing,
// and error handling for ToolHive's dynamic webhook middleware system.
package webhook

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// APIVersion is the version of the webhook API protocol.
const APIVersion = "v0.1.0"

// DefaultTimeout is the default timeout for webhook HTTP calls.
const DefaultTimeout = 10 * time.Second

// MaxTimeout is the maximum allowed timeout for webhook HTTP calls.
const MaxTimeout = 30 * time.Second

// MaxResponseSize is the maximum allowed size in bytes for webhook responses (1 MB).
const MaxResponseSize = 1 << 20

// Type indicates whether a webhook is validating or mutating.
type Type string

const (
	// TypeValidating indicates a validating webhook that accepts or denies requests.
	TypeValidating Type = "validating"
	// TypeMutating indicates a mutating webhook that transforms requests.
	TypeMutating Type = "mutating"
)

// FailurePolicy defines how webhook errors are handled.
type FailurePolicy string

const (
	// FailurePolicyFail denies the request on webhook error (fail-closed).
	FailurePolicyFail FailurePolicy = "fail"
	// FailurePolicyIgnore allows the request on webhook error (fail-open).
	FailurePolicyIgnore FailurePolicy = "ignore"
)

// TLSConfig holds TLS-related configuration for webhook HTTP communication.
type TLSConfig struct {
	// CABundlePath is the path to a CA certificate bundle for server verification.
	CABundlePath string `json:"ca_bundle_path,omitempty"`
	// ClientCertPath is the path to a client certificate for mTLS.
	ClientCertPath string `json:"client_cert_path,omitempty"`
	// ClientKeyPath is the path to a client key for mTLS.
	ClientKeyPath string `json:"client_key_path,omitempty"`
	// InsecureSkipVerify disables server certificate verification.
	// WARNING: This should only be used for development/testing.
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
}

// Config holds the configuration for a single webhook.
type Config struct {
	// Name is a unique identifier for this webhook.
	Name string `json:"name"`
	// URL is the HTTPS endpoint to call.
	URL string `json:"url"`
	// Timeout is the maximum time to wait for a webhook response.
	Timeout time.Duration `json:"timeout"`
	// FailurePolicy determines behavior when the webhook call fails.
	FailurePolicy FailurePolicy `json:"failure_policy"`
	// TLSConfig holds optional TLS configuration (CA bundles, client certs).
	TLSConfig *TLSConfig `json:"tls_config,omitempty"`
	// HMACSecretRef is an optional reference to an HMAC secret for payload signing.
	HMACSecretRef string `json:"hmac_secret_ref,omitempty"`
}

// Validate checks that the WebhookConfig has valid required fields.
func (c *Config) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("webhook name is required")
	}
	if c.URL == "" {
		return fmt.Errorf("webhook URL is required")
	}
	if _, err := url.ParseRequestURI(c.URL); err != nil {
		return fmt.Errorf("webhook URL is invalid: %w", err)
	}
	if c.FailurePolicy != FailurePolicyFail && c.FailurePolicy != FailurePolicyIgnore {
		return fmt.Errorf("webhook failure_policy must be %q or %q, got %q",
			FailurePolicyFail, FailurePolicyIgnore, c.FailurePolicy)
	}
	if c.Timeout < 0 {
		return fmt.Errorf("webhook timeout must be non-negative")
	}
	if c.Timeout > MaxTimeout {
		return fmt.Errorf("webhook timeout %v exceeds maximum %v", c.Timeout, MaxTimeout)
	}
	if c.TLSConfig != nil {
		if err := validateTLSConfig(c.TLSConfig); err != nil {
			return fmt.Errorf("webhook TLS config: %w", err)
		}
	}
	return nil
}

// Request is the payload sent to webhook endpoints.
type Request struct {
	// Version is the webhook API protocol version.
	Version string `json:"version"`
	// UID is a unique identifier for this request, used for idempotency.
	UID string `json:"uid"`
	// Timestamp is when the request was created.
	Timestamp time.Time `json:"timestamp"`
	// Principal contains the authenticated user's identity information.
	Principal *Principal `json:"principal"`
	// MCPRequest is the raw MCP JSON-RPC request.
	MCPRequest json.RawMessage `json:"mcp_request"`
	// Context provides additional metadata about the request origin.
	Context *RequestContext `json:"context"`
}

// Principal contains the authenticated user's identity information.
type Principal struct {
	// Sub is the subject identifier (user ID).
	Sub string `json:"sub"`
	// Email is the user's email address.
	Email string `json:"email,omitempty"`
	// Name is the user's display name.
	Name string `json:"name,omitempty"`
	// Groups is a list of groups the user belongs to.
	Groups []string `json:"groups,omitempty"`
	// Claims contains additional identity claims.
	Claims map[string]string `json:"claims,omitempty"`
}

// RequestContext provides metadata about the request origin and environment.
type RequestContext struct {
	// ServerName is the ToolHive/vMCP instance name handling the request.
	ServerName string `json:"server_name"`
	// BackendServer is the actual MCP server being proxied (when using vMCP).
	BackendServer string `json:"backend_server,omitempty"`
	// Namespace is the Kubernetes namespace, if applicable.
	Namespace string `json:"namespace,omitempty"`
	// SourceIP is the client's IP address.
	SourceIP string `json:"source_ip"`
	// Transport is the connection transport type (e.g., "sse", "stdio").
	Transport string `json:"transport"`
}

// Response is the response from a validating webhook.
type Response struct {
	// Version is the webhook API protocol version.
	Version string `json:"version"`
	// UID is the unique request identifier, echoed back for correlation.
	UID string `json:"uid"`
	// Allowed indicates whether the request is permitted.
	Allowed bool `json:"allowed"`
	// Code is an optional HTTP status code for denied requests.
	Code int `json:"code,omitempty"`
	// Message is an optional human-readable explanation.
	Message string `json:"message,omitempty"`
	// Reason is an optional machine-readable denial reason.
	Reason string `json:"reason,omitempty"`
	// Details contains optional structured information about the denial.
	Details map[string]string `json:"details,omitempty"`
}

// MutatingResponse is the response from a mutating webhook.
type MutatingResponse struct {
	Response
	// PatchType indicates the type of patch (e.g., "json_patch").
	PatchType string `json:"patch_type,omitempty"`
	// Patch contains the JSON Patch operations to apply.
	Patch json.RawMessage `json:"patch,omitempty"`
}

// validateTLSConfig validates the TLS configuration for consistency.
func validateTLSConfig(cfg *TLSConfig) error {
	// If one of client cert/key is provided, both must be present.
	if (cfg.ClientCertPath == "") != (cfg.ClientKeyPath == "") {
		return fmt.Errorf("both client_cert_path and client_key_path must be provided for mTLS")
	}
	return nil
}
