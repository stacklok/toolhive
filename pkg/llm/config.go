// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultProxyListenPort is the default port the localhost proxy listens on.
	DefaultProxyListenPort = 14000

	// DefaultOIDCScopes are the default OAuth scopes requested during login.
	DefaultOIDCScopes = "openid offline_access"
)

// LLMConfig holds all LLM gateway settings persisted under the llm: key in
// ToolHive's config.yaml.
type LLMConfig struct {
	GatewayURL      string          `yaml:"gateway_url,omitempty"`
	OIDC            LLMOIDCConfig   `yaml:"oidc,omitempty"`
	Proxy           LLMProxyConfig  `yaml:"proxy,omitempty"`
	Auth            LLMAuthState    `yaml:"auth,omitempty"`
	ConfiguredTools []LLMToolConfig `yaml:"configured_tools,omitempty"`
}

// LLMOIDCConfig holds OIDC provider settings for the LLM gateway.
type LLMOIDCConfig struct {
	Issuer       string   `yaml:"issuer,omitempty"`
	ClientID     string   `yaml:"client_id,omitempty"`
	Scopes       []string `yaml:"scopes,omitempty"`
	Audience     string   `yaml:"audience,omitempty"`
	CallbackPort int      `yaml:"callback_port,omitempty"`
}

// LLMProxyConfig holds configuration for the localhost reverse proxy.
type LLMProxyConfig struct {
	ListenPort int `yaml:"listen_port,omitempty"`
}

// LLMAuthState records token lifecycle metadata persisted to config (no token
// values — those live in the secrets provider or memory only).
type LLMAuthState struct {
	// CachedTokenExpiry is the expiry time of the most recently cached access
	// token. Used to surface helpful messages when the token is about to expire.
	CachedTokenExpiry time.Time `yaml:"cached_token_expiry,omitempty"`
}

// LLMToolConfig records a tool that setup has configured, so teardown knows
// exactly what to reverse.
type LLMToolConfig struct {
	// Tool is the canonical tool identifier (e.g. "claude-code", "cursor").
	Tool string `yaml:"tool"`
	// Mode is the authentication mode: "direct" or "proxy".
	Mode string `yaml:"mode"`
	// ConfigPath is the absolute path to the tool's config file that was patched.
	ConfigPath string `yaml:"config_path"`
}

// IsConfigured reports whether the minimum required fields are present for the
// LLM gateway to be usable: gateway URL, OIDC issuer, and OIDC client ID.
func (c *LLMConfig) IsConfigured() bool {
	return c.GatewayURL != "" && c.OIDC.Issuer != "" && c.OIDC.ClientID != ""
}

// Validate performs full validation of the LLM config, including HTTPS
// enforcement, port range checks, and OIDC field requirements.
func (c *LLMConfig) Validate() error {
	var errs []error

	if c.GatewayURL == "" {
		errs = append(errs, errors.New("gateway_url is required"))
	} else if !strings.HasPrefix(c.GatewayURL, "https://") {
		errs = append(errs, fmt.Errorf("gateway_url must use HTTPS, got: %s", c.GatewayURL))
	}

	if c.OIDC.Issuer == "" {
		errs = append(errs, errors.New("oidc.issuer is required"))
	}

	if c.OIDC.ClientID == "" {
		errs = append(errs, errors.New("oidc.client_id is required"))
	}

	if c.Proxy.ListenPort != 0 && (c.Proxy.ListenPort < 1024 || c.Proxy.ListenPort > 65535) {
		errs = append(errs, fmt.Errorf("proxy.listen_port must be between 1024 and 65535, got: %d", c.Proxy.ListenPort))
	}

	if c.OIDC.CallbackPort != 0 && (c.OIDC.CallbackPort < 1024 || c.OIDC.CallbackPort > 65535) {
		errs = append(errs, fmt.Errorf("oidc.callback_port must be between 1024 and 65535, got: %d", c.OIDC.CallbackPort))
	}

	return errors.Join(errs...)
}

// EffectiveProxyPort returns the configured proxy listen port, or
// DefaultProxyListenPort if none is set.
func (c *LLMConfig) EffectiveProxyPort() int {
	if c.Proxy.ListenPort > 0 {
		return c.Proxy.ListenPort
	}
	return DefaultProxyListenPort
}

// EffectiveScopes returns the configured OIDC scopes, or the default scopes
// (openid, offline_access) if none are set.
func (c *LLMOIDCConfig) EffectiveScopes() []string {
	if len(c.Scopes) > 0 {
		return c.Scopes
	}
	return strings.Fields(DefaultOIDCScopes)
}
