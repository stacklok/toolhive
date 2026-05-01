// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/networking"
	pkgoidc "github.com/stacklok/toolhive/pkg/oidc"
)

// maxAnthropicPathPrefixLen caps the AnthropicPathPrefix length to a value
// large enough for any realistic gateway route while preventing absurd inputs
// from being persisted into ToolHive config or Claude Code's settings file.
const maxAnthropicPathPrefixLen = 256

// DefaultAnthropicPathPrefix is the path appended to the gateway URL when the
// user has not explicitly set llm.anthropic_path_prefix. It targets Envoy AI
// Gateway, which routes native-Anthropic traffic at /anthropic/v1/messages.
// Users on LiteLLM or direct Anthropic must opt out with
// `--anthropic-path-prefix=""` so EffectiveAnthropicPathPrefix returns "".
const DefaultAnthropicPathPrefix = "/anthropic"

const (
	// DefaultProxyListenPort is the default port the localhost proxy listens on.
	DefaultProxyListenPort = 14000
)

// OIDCConfig is a type alias for oidc.ClientConfig, holding OIDC provider
// settings and cached token state for the LLM gateway. Using a type alias
// ensures this type stays in sync with pkg/config.RegistryOAuthConfig, which
// is also an alias for the same underlying type.
type OIDCConfig = pkgoidc.ClientConfig

// Config holds all LLM gateway settings persisted under the llm: key in
// ToolHive's config.yaml.
type Config struct {
	GatewayURL    string `yaml:"gateway_url,omitempty"       json:"gateway_url,omitempty"`
	TLSSkipVerify bool   `yaml:"tls_skip_verify,omitempty"   json:"tls_skip_verify,omitempty"`
	// AnthropicPathPrefix is appended to GatewayURL when writing
	// ANTHROPIC_BASE_URL for direct-mode tools. nil means "use the default"
	// (DefaultAnthropicPathPrefix, which targets Envoy AI Gateway). An
	// explicit empty string means "no prefix" — required for LiteLLM or direct
	// Anthropic. Use EffectiveAnthropicPathPrefix to read the resolved value.
	// Must start with "/" when non-nil and non-empty.
	AnthropicPathPrefix *string      `yaml:"anthropic_path_prefix,omitempty" json:"anthropic_path_prefix,omitempty"`
	OIDC                OIDCConfig   `yaml:"oidc,omitempty"              json:"oidc,omitempty"`
	Proxy               ProxyConfig  `yaml:"proxy,omitempty"             json:"proxy,omitempty"`
	ConfiguredTools     []ToolConfig `yaml:"configured_tools,omitempty"  json:"configured_tools,omitempty"`
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

	if err := validateAnthropicPathPrefix(c.AnthropicPathPrefix); err != nil {
		errs = append(errs, fmt.Errorf("anthropic_path_prefix: %w", err))
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

// EffectiveAnthropicPathPrefix returns the path prefix that should be appended
// to the gateway URL when writing ANTHROPIC_BASE_URL. A nil persisted value
// means "use the default" (DefaultAnthropicPathPrefix, targeting Envoy AI
// Gateway). An explicit empty string disables the prefix for LiteLLM or
// direct Anthropic.
func (c *Config) EffectiveAnthropicPathPrefix() string {
	if c.AnthropicPathPrefix == nil {
		return DefaultAnthropicPathPrefix
	}
	return *c.AnthropicPathPrefix
}

// validateAnthropicPathPrefix enforces that the prefix, when non-nil and
// non-empty, is a well-formed URL path: starts with "/", contains no query
// string, fragment, or shell-unsafe characters, and stays under
// maxAnthropicPathPrefixLen. nil means "use the default" and an explicit
// empty string means "no prefix" — both are valid. The shell check is
// defensive: the value flows into a Node-process env var, not a shell
// command, but rejecting metacharacters keeps surprises out of any future
// caller that does invoke a shell.
func validateAnthropicPathPrefix(p *string) error {
	if p == nil || *p == "" {
		return nil
	}
	v := *p
	if len(v) > maxAnthropicPathPrefixLen {
		return fmt.Errorf("must be %d characters or fewer, got %d", maxAnthropicPathPrefixLen, len(v))
	}
	if !strings.HasPrefix(v, "/") {
		return fmt.Errorf("must start with %q, got %q", "/", v)
	}
	if strings.ContainsAny(v, "?#") {
		return fmt.Errorf("must not contain a query string or fragment, got %q", v)
	}
	const shellUnsafe = `"\;$` + "`\n\r '"
	if strings.ContainsAny(v, shellUnsafe) {
		return fmt.Errorf("must not contain whitespace or shell metacharacters, got %q", v)
	}
	return nil
}
