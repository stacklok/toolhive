// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

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
	if opts.Scopes != nil {
		c.OIDC.Scopes = requestedScopes(opts.Scopes)
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
	if opts.ShortPromptCache != nil {
		c.ShortPromptCache = *opts.ShortPromptCache
	}
	if opts.BedrockCompat != nil {
		c.Bedrock.Compat = *opts.BedrockCompat
	}
	if opts.Enable1M != nil {
		c.Bedrock.Enable1M = *opts.Enable1M
	}
	if opts.Models != nil {
		c.Models = opts.Models
	}

	if !c.IsConfigured() {
		return c.ValidatePartial()
	}
	return c.Validate()
}

// SetOptions carries the flag values for the "config set" command.
// Zero values are treated as "not provided" and leave the existing config
// field unchanged. TLSSkipVerify uses a pointer so that false can be
// distinguished from "not provided" (enabling explicit clear via config set).
type SetOptions struct {
	GatewayURL string
	Issuer     string
	ClientID   string
	Audience   string
	// Scopes are the OIDC scopes to request besides offline_access, which is
	// always requested so the provider returns a refresh token. nil = not
	// provided (leave existing config unchanged); an empty non-nil slice
	// resets to the default scopes.
	Scopes           []string
	ProxyPort        int
	CallbackPort     int
	TLSSkipVerify    *bool // nil = not provided; &false = explicitly disable
	ShortPromptCache *bool // nil = not provided; &false = explicitly restore one-hour caching
	// BedrockCompat and Enable1M use pointers so false can be distinguished from
	// "not provided" (enabling explicit clear via config set). See BedrockConfig.
	BedrockCompat *bool
	Enable1M      *bool
	// Models sets the persisted model IDs (Config.Models). nil = not provided
	// (leave existing config unchanged); an empty non-nil slice clears them.
	Models []string
}

// requestedScopes returns offline_access followed by extra, without blanks or
// duplicates. It returns nil for an empty extra, so a reset leaves the config
// field unset and EffectiveScopes falls back to the default scopes.
func requestedScopes(extra []string) []string {
	var wanted []string
	for _, s := range extra {
		if s = strings.TrimSpace(s); s != "" {
			wanted = append(wanted, s)
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	scopes := []string{"offline_access"}
	for _, s := range wanted {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	return scopes
}

// ConfigSet applies opts to the persisted LLM config. When that changes the
// requested scopes it also drops the cached tokens (see
// invalidateTokensOnScopeChange). The tokens are dropped only after the new
// config is written, so a rejected value leaves both untouched.
func ConfigSet(
	ctx context.Context, errOut io.Writer, provider ConfigUpdater, opts SetOptions, getSecrets SecretsProviderFunc,
) error {
	var before, after Config
	if err := provider.UpdateLLMConfig(func(c *Config) error {
		before = *c
		if err := c.SetFields(opts); err != nil {
			return err
		}
		after = *c
		return nil
	}); err != nil {
		return err
	}
	return invalidateTokensOnScopeChange(ctx, errOut, provider, before, after, getSecrets)
}

// scopesChanged reports whether the two configs request different scope sets.
// Order is ignored: it does not change what the provider grants.
func scopesChanged(before, after Config) bool {
	sorted := func(c Config) []string { return slices.Sorted(slices.Values(c.OIDC.EffectiveScopes())) }
	return !slices.Equal(sorted(before), sorted(after))
}

// invalidateTokensOnScopeChange drops the cached OIDC tokens and the persisted
// reference to them when after requests different scopes than before. A
// refresh-token grant returns only the scopes the token was first issued with,
// so a changed list reaches the user only through a fresh login; with the cache
// gone, the next login requests the new scopes. It does nothing when the scopes
// are unchanged, so re-running a command keeps the existing session.
func invalidateTokensOnScopeChange(
	ctx context.Context, errOut io.Writer, provider ConfigUpdater, before, after Config,
	getSecrets SecretsProviderFunc,
) error {
	if !scopesChanged(before, after) {
		return nil
	}
	secretsProvider, err := getSecrets()
	if err != nil {
		return fmt.Errorf("failed to get secrets provider: %w", err)
	}
	if err := DeleteCachedTokens(ctx, secretsProvider); err != nil {
		return fmt.Errorf("deleting cached tokens for the changed scopes: %w", err)
	}
	if err := provider.UpdateLLMConfig(func(c *Config) error {
		c.OIDC.CachedRefreshTokenRef = ""
		c.OIDC.CachedTokenExpiry = time.Time{}
		return nil
	}); err != nil {
		return fmt.Errorf("clearing the cached token reference: %w", err)
	}
	_, _ = fmt.Fprintln(errOut, "Scopes changed; cached tokens cleared so the next sign-in requests the new scopes.")
	_, _ = fmt.Fprintln(errOut,
		"Stop any running LLM proxy or token helper and start it again; an active process may recreate "+
			"cached credentials issued for the old scopes.")
	return nil
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
	writef("OIDC Issuer:     %s\n", c.OIDC.Issuer)
	writef("OIDC Client:     %s\n", c.OIDC.ClientID)
	if c.OIDC.Audience != "" {
		writef("Audience:        %s\n", c.OIDC.Audience)
	}
	writef("Proxy Port:      %d\n", c.EffectiveProxyPort())
	writef("Scopes:          %v\n", c.OIDC.EffectiveScopes())
	promptCacheTTL := "1h"
	if c.ShortPromptCache {
		promptCacheTTL = "5m"
	}
	writef("Prompt Cache:    %s\n", promptCacheTTL)
	if c.TLSSkipVerify {
		writef("TLS Skip Verify: true (WARNING: certificate verification disabled)\n")
	}
	if len(c.Models) > 0 {
		writef("Models:          %v\n", c.Models)
	}
	if c.Bedrock.Compat {
		writef("Bedrock compat:  true (CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1)\n")
		writef("  1M context:    %t\n", c.Bedrock.Enable1M)
	}
	if len(c.ConfiguredTools) > 0 {
		writef("Configured tools:\n")
		for _, t := range c.ConfiguredTools {
			writef("  - %s (%s)  %s\n", t.Tool, t.Mode, t.ConfigPath)
		}
	}
	return err
}
