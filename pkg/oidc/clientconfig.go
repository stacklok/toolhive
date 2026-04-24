// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package oidc

import (
	"strings"
	"time"
)

const (
	// DefaultScopes are the default OAuth scopes requested during login.
	DefaultScopes = "openid offline_access"
)

// ClientConfig holds the OIDC provider settings and cached token state shared
// by both registry OAuth and LLM gateway authentication flows. Token values
// are never stored here — only references and expiry metadata.
//
// Both pkg/config.RegistryOAuthConfig and pkg/llm.OIDCConfig are type aliases
// for this type, so validation logic and new fields stay in sync across both
// authentication flows.
type ClientConfig struct {
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

// EffectiveScopes returns the configured OIDC scopes, or the default scopes
// (openid, offline_access) if none are set.
func (c *ClientConfig) EffectiveScopes() []string {
	if len(c.Scopes) > 0 {
		return c.Scopes
	}
	return strings.Fields(DefaultScopes)
}
