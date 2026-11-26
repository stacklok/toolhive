// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"strings"
)

// UpstreamProvidersForTokenLoad returns the upstream provider names the vMCP
// needs loaded for a request: every provider named by a backend auth strategy
// (token_exchange, upstream_inject, obo, aws_sts and xaa, over the default
// strategy and every backend) plus the Cedar authorizer's
// PrimaryUpstreamProvider when set. Names are deduplicated and sorted.
//
// Call it after InjectSubjectProviderNames: before injection, a strategy that
// relies on the default upstream has an empty name and contributes nothing.
// It only reads cfg.
//
// Nothing calls it yet: it is not wired into thv vmcp serve or the runner.
//
// A nil result means the caller must load every provider, which is what
// upstreamtoken.WithProviders does when given no names: outgoing auth has no
// Default strategy or its source is "discovered", or no provider name was
// found. Without a Default, a backend with no entry of its own keeps the
// strategy it discovered, which cfg does not list.
//
// It covers vMCP only. The runner's middleware (pkg/auth/obo/middleware.go and
// pkg/auth/upstreamswap/middleware.go) reads provider names from its own
// config, so the runner must derive its own list; this helper does not cover
// them.
func UpstreamProvidersForTokenLoad(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	out := cfg.OutgoingAuth
	if out == nil || out.Default == nil || out.Source == "discovered" {
		return nil
	}

	seen := map[string]struct{}{}
	add := func(name string) {
		if name = strings.TrimSpace(name); name != "" {
			seen[name] = struct{}{}
		}
	}
	add(out.Default.SubjectProviderName())
	for _, strategy := range out.Backends {
		add(strategy.SubjectProviderName())
	}
	if cfg.IncomingAuth != nil && cfg.IncomingAuth.Authz != nil {
		add(cfg.IncomingAuth.Authz.PrimaryUpstreamProvider)
	}

	if len(seen) == 0 {
		return nil
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}
