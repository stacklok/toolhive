// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"
	"io"

	pkgsecrets "github.com/stacklok/toolhive/pkg/secrets"
)

// SetFields applies the non-zero fields from the provided options to the
// config and validates the result. If the mandatory trio (gateway_url,
// oidc.issuer, oidc.client_id) is present after the update, full validation
// runs; otherwise only format/range validation runs to catch bad values early
// while still allowing incremental configuration.
func (c *Config) SetFields(opts SetOptions) error {
	if opts.GatewayURL != "" {
		c.GatewayURL = opts.GatewayURL
	}
	if opts.Issuer != "" {
		c.OIDC.Issuer = opts.Issuer
	}
	if opts.ClientID != "" {
		c.OIDC.ClientID = opts.ClientID
	}
	if opts.Audience != "" {
		c.OIDC.Audience = opts.Audience
	}
	if opts.ProxyPort != 0 {
		c.Proxy.ListenPort = opts.ProxyPort
	}
	if opts.CallbackPort != 0 {
		c.OIDC.CallbackPort = opts.CallbackPort
	}
	if opts.TLSSkipVerify != nil {
		c.TLSSkipVerify = *opts.TLSSkipVerify
	}
	if opts.AnthropicPathPrefix != nil {
		c.AnthropicPathPrefix = opts.AnthropicPathPrefix
	}

	if !c.IsConfigured() {
		return c.ValidatePartial()
	}
	return c.Validate()
}

// SetOptions carries the flag values for the "config set" command.
// Zero values are treated as "not provided" and leave the existing config
// field unchanged. Pointer-typed fields use nil for "not provided" so that
// the zero value (false, "") can still be set explicitly — required for
// TLSSkipVerify=false (clear) and AnthropicPathPrefix="" (opt out of the
// /anthropic default for LiteLLM or direct Anthropic).
type SetOptions struct {
	GatewayURL          string
	Issuer              string
	ClientID            string
	Audience            string
	ProxyPort           int
	CallbackPort        int
	TLSSkipVerify       *bool   // nil = not provided; &false = explicitly disable
	AnthropicPathPrefix *string // nil = not provided; &"" = explicit no prefix; &"/x" = explicit value
}

// DeleteCachedTokens removes all cached OIDC tokens stored under the LLM
// scope via the provided secrets provider. It is a no-op if the provider does
// not support listing or deletion (e.g. the environment provider), since such
// providers cannot hold cached tokens.
func DeleteCachedTokens(ctx context.Context, provider pkgsecrets.Provider) error {
	scoped := pkgsecrets.NewScopedProvider(provider, pkgsecrets.ScopeLLM)
	caps := scoped.Capabilities()
	if !caps.CanList || !caps.CanDelete {
		return nil
	}
	descs, err := scoped.ListSecrets(ctx)
	if err != nil {
		return err
	}
	if len(descs) == 0 {
		return nil
	}
	names := make([]string, len(descs))
	for i, d := range descs {
		names[i] = d.Key
	}
	return scoped.DeleteSecrets(ctx, names)
}

// Show writes a human-readable representation of the config to w.
// If the config is not yet configured it prints a hint to run "config set".
func (c *Config) Show(w io.Writer) error {
	if !c.IsConfigured() {
		_, err := fmt.Fprintln(w, "LLM gateway is not configured. Run \"thv llm config set\" to configure it.")
		return err
	}

	var err error
	writef := func(format string, args ...any) {
		if err == nil {
			_, err = fmt.Fprintf(w, format, args...)
		}
	}

	writef("Gateway URL:     %s\n", c.GatewayURL)
	switch {
	case c.AnthropicPathPrefix == nil:
		writef("Anthropic path:  %s (default)\n", DefaultAnthropicPathPrefix)
	case *c.AnthropicPathPrefix == "":
		writef("Anthropic path:  (none — direct Anthropic / LiteLLM)\n")
	default:
		writef("Anthropic path:  %s\n", *c.AnthropicPathPrefix)
	}
	writef("OIDC Issuer:     %s\n", c.OIDC.Issuer)
	writef("OIDC Client:     %s\n", c.OIDC.ClientID)
	if c.OIDC.Audience != "" {
		writef("Audience:        %s\n", c.OIDC.Audience)
	}
	writef("Proxy Port:      %d\n", c.EffectiveProxyPort())
	writef("Scopes:          %v\n", c.OIDC.EffectiveScopes())
	if c.TLSSkipVerify {
		writef("TLS Skip Verify: true (WARNING: certificate verification disabled)\n")
	}
	if len(c.ConfiguredTools) > 0 {
		writef("Configured tools:\n")
		for _, t := range c.ConfiguredTools {
			writef("  - %s (%s)  %s\n", t.Tool, t.Mode, t.ConfigPath)
		}
	}
	return err
}
