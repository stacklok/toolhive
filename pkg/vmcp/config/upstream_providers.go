// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"

	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
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
// filter is false when the caller must load every provider: the outgoing auth
// source is "discovered" (backends carry their own strategies that cfg does
// not list), or no provider name was found.
func UpstreamProvidersForTokenLoad(cfg *Config) (names []string, filter bool) {
	if cfg == nil {
		return nil, false
	}
	if cfg.OutgoingAuth != nil && cfg.OutgoingAuth.Source == "discovered" {
		return nil, false
	}

	seen := map[string]struct{}{}
	add := func(name string) {
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, strategy := range collectAllBackendStrategies(cfg) {
		add(strategyProviderName(strategy))
	}
	if cfg.IncomingAuth != nil && cfg.IncomingAuth.Authz != nil {
		add(cfg.IncomingAuth.Authz.PrimaryUpstreamProvider)
	}

	if len(seen) == 0 {
		return nil, false
	}
	names = make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	slices.Sort(names)
	return names, true
}

func strategyProviderName(s *authtypes.BackendAuthStrategy) string {
	if s == nil {
		return ""
	}
	switch s.Type {
	case authtypes.StrategyTypeTokenExchange:
		if s.TokenExchange != nil {
			return s.TokenExchange.SubjectProviderName
		}
	case authtypes.StrategyTypeUpstreamInject:
		if s.UpstreamInject != nil {
			return s.UpstreamInject.ProviderName
		}
	case authtypes.StrategyTypeOBO:
		if s.OBO != nil {
			return s.OBO.SubjectTokenProviderName
		}
	case authtypes.StrategyTypeAwsSts:
		if s.AwsSts != nil {
			return s.AwsSts.SubjectProviderName
		}
	case authtypes.StrategyTypeXAA:
		if s.XAA != nil {
			return s.XAA.SubjectProviderName
		}
	}
	return ""
}
