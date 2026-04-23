// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/networking"
)

const (
	// DefaultProxyListenPort is the default port the localhost proxy listens on.
	DefaultProxyListenPort = 14000

	// DefaultOIDCScopes are the default OAuth scopes requested during login.
	DefaultOIDCScopes = "openid offline_access"
)

// Config holds all LLM gateway settings persisted under the llm: key in
// ToolHive's config.yaml.
type Config struct {
	GatewayURL      string       `yaml:"gateway_url,omitempty"       json:"gateway_url,omitempty"`
	OIDC            OIDCConfig   `yaml:"oidc,omitempty"              json:"oidc,omitempty"`
	Proxy           ProxyConfig  `yaml:"proxy,omitempty"             json:"proxy,omitempty"`
	ConfiguredTools []ToolConfig `yaml:"configured_tools,omitempty"  json:"configured_tools,omitempty"`
}

// OIDCConfig holds OIDC provider settings and cached token state for the LLM
// gateway. Cached fields follow the same pattern as RegistryOAuthConfig in
// pkg/config/config.go — token values are never stored here, only references
// and expiry metadata.
type OIDCConfig struct {
	Issuer       string   `yaml:"issuer,omitempty"        json:"issuer,omitempty"`
	ClientID     string   `yaml:"client_id,omitempty"     json:"client_id,omitempty"`
	Scopes       []string `yaml:"scopes,omitempty"        json:"scopes,omitempty"`
	Audience     string   `yaml:"audience,omitempty"      json:"audience,omitempty"`
	CallbackPort int      `yaml:"callback_port,omitempty" json:"callback_port,omitempty"`

	// CachedRefreshTokenRef is the secrets-provider key under which the refresh
	// token is stored (never the token value itself).
	CachedRefreshTokenRef string `yaml:"cached_refresh_token_ref,omitempty" json:"cached_refresh_token_ref,omitempty"`
	// CachedTokenExpiry is the expiry of the most recently cached access token,
	// used to surface helpful messages when the token is about to expire.
	CachedTokenExpiry time.Time `yaml:"cached_token_expiry,omitempty" json:"cached_token_expiry,omitempty"`
}

// ProxyConfig holds configuration for the localhost reverse proxy.
type ProxyConfig struct {
	ListenPort int `yaml:"listen_port,omitempty" json:"listen_port,omitempty"`
}

// ToolConfig records a tool that setup has configured, so teardown knows
// exactly what to reverse.
type ToolConfig struct {
	// Tool is the canonical tool identifier (e.g. "claude-code", "cursor").
	Tool string `yaml:"tool" json:"tool"`
	// Mode is the authentication mode: "direct" or "proxy".
	Mode string `yaml:"mode" json:"mode"`
	// ConfigPath is the absolute path to the tool's config file that was patched.
	ConfigPath string `yaml:"config_path" json:"config_path"`
}

// IsConfigured reports whether the minimum required fields are present for the
// LLM gateway to be usable: gateway URL, OIDC issuer, and OIDC client ID.
func (c *Config) IsConfigured() bool {
	return c.GatewayURL != "" && c.OIDC.Issuer != "" && c.OIDC.ClientID != ""
}

// ValidatePartial validates any fields that are explicitly set, without
// requiring the mandatory trio (gateway_url, oidc.issuer, oidc.client_id).
// Use this to catch URL format or port range errors during incremental
// configuration, before all required fields have been provided.
func (c *Config) ValidatePartial() error {
	var errs []error

	if c.GatewayURL != "" {
		if err := networking.ValidateHTTPSURL(c.GatewayURL); err != nil {
			errs = append(errs, fmt.Errorf("gateway_url: %w", err))
		}
	}

	if c.OIDC.Issuer != "" {
		if err := networking.ValidateIssuerURL(c.OIDC.Issuer); err != nil {
			errs = append(errs, fmt.Errorf("oidc.issuer: %w", err))
		}
	}

	if c.Proxy.ListenPort != 0 && (c.Proxy.ListenPort < 1024 || c.Proxy.ListenPort > 65535) {
		errs = append(errs, fmt.Errorf("proxy.listen_port must be between 1024 and 65535, got: %d", c.Proxy.ListenPort))
	}

	// Reuse networking.ValidateCallbackPort for the OIDC callback port — same
	// range check (1024–65535), zero means ephemeral (auto-assigned). Pass the
	// client ID so the validator applies strict availability checking for
	// pre-registered clients (clientID != "").
	if err := networking.ValidateCallbackPort(c.OIDC.CallbackPort, c.OIDC.ClientID); err != nil {
		errs = append(errs, fmt.Errorf("oidc.callback_port: %w", err))
	}

	return errors.Join(errs...)
}

// Validate performs full validation of the LLM config, including HTTPS
// enforcement, port range checks, and OIDC field requirements.
func (c *Config) Validate() error {
	var errs []error

	if c.GatewayURL == "" {
		errs = append(errs, errors.New("gateway_url is required"))
	}

	if c.OIDC.Issuer == "" {
		errs = append(errs, errors.New("oidc.issuer is required"))
	}

	if c.OIDC.ClientID == "" {
		errs = append(errs, errors.New("oidc.client_id is required"))
	}

	return errors.Join(append(errs, c.ValidatePartial())...)
}

// EffectiveProxyPort returns the configured proxy listen port, or
// DefaultProxyListenPort if none is set.
func (c *Config) EffectiveProxyPort() int {
	if c.Proxy.ListenPort > 0 {
		return c.Proxy.ListenPort
	}
	return DefaultProxyListenPort
}

// EffectiveScopes returns the configured OIDC scopes, or the default scopes
// (openid, offline_access) if none are set.
func (c *OIDCConfig) EffectiveScopes() []string {
	if len(c.Scopes) > 0 {
		return c.Scopes
	}
	return strings.Fields(DefaultOIDCScopes)
}
