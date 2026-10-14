// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package upstreamfilter

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/claims"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
)

const defaultClaimName = "groups"

// GroupBasedFilter implements handlers.UpstreamFilter by matching the
// principal's group claim against configured rules and returning the union of
// the matching rules' upstream provider sets. See package documentation and
// GroupBasedFilterConfig for semantics.
type GroupBasedFilter struct {
	claim            string
	rules            []GroupRule
	defaultUpstreams []string
}

// Compile-time assertion that GroupBasedFilter satisfies the UpstreamFilter
// contract.
var _ handlers.UpstreamFilter = (*GroupBasedFilter)(nil)

// NewGroupBasedFilter constructs and validates a GroupBasedFilter.
//
// configuredUpstreams is the full ordered list of upstream provider names
// declared on the auth server, including the mandatory first upstream at
// index 0. It is required at construction time to validate that every
// referenced provider exists and that no rule or default references the
// mandatory first upstream (which is always included and cannot be filtered).
//
// It returns an error for any of: fewer than two configured upstreams; no
// rules; empty or duplicate group values within a rule; empty or duplicate
// upstream references within a rule; unknown upstream references; references
// to the mandatory first upstream; unknown or first-upstream references in
// DefaultUpstreams.
func NewGroupBasedFilter(cfg GroupBasedFilterConfig, configuredUpstreams []string) (*GroupBasedFilter, error) {
	if len(configuredUpstreams) < 2 {
		return nil, fmt.Errorf(
			"group-based upstream filter requires at least two configured upstreams, got %d",
			len(configuredUpstreams),
		)
	}
	if len(cfg.Rules) == 0 {
		return nil, errors.New("group-based upstream filter requires at least one rule")
	}

	claim := cfg.Claim
	if claim == "" {
		claim = defaultClaimName
	}

	firstUpstream := configuredUpstreams[0]
	validProviders := make(map[string]struct{}, len(configuredUpstreams)-1)
	for _, name := range configuredUpstreams[1:] {
		validProviders[name] = struct{}{}
	}

	for i, rule := range cfg.Rules {
		if err := validateRule(rule, i, firstUpstream, validProviders); err != nil {
			return nil, err
		}
	}
	if err := validateProviderList("defaultUpstreams", cfg.DefaultUpstreams, firstUpstream, validProviders); err != nil {
		return nil, err
	}

	return &GroupBasedFilter{
		claim:            claim,
		rules:            cloneRules(cfg.Rules),
		defaultUpstreams: append([]string(nil), cfg.DefaultUpstreams...),
	}, nil
}

// FilterUpstreams implements handlers.UpstreamFilter.
//
// It reads principal.Claims[filter.claim], decodes it as a []string via the
// shared claims helper, and returns the union of every matching rule's
// UpstreamProviders. The claim MUST be present and MUST be a JSON string
// array — a missing, nil, non-array, or mixed-type claim value fails the
// authorization per the filter contract's fail-closed guarantee for
// security-relevant filters.
//
// A valid empty array or a valid array with no matching rule falls back to
// the configured DefaultUpstreams. Duplicate names across matching rules and
// defaults are deduplicated in the returned slice; the handler layer preserves
// configured order and drops any name that is not a non-first configured
// upstream.
//
// principal (including its Claims map) is treated as read-only. Claim values
// are not logged.
//
// The configured argument is unused: the constructor validated every rule and
// default reference against the full configured upstream list, so runtime
// reconciliation of the returned set against the non-first upstreams is
// redundant with the handler layer's own drop-unknown behavior.
func (f *GroupBasedFilter) FilterUpstreams(
	_ context.Context,
	principal auth.PrincipalInfo,
	_ []string,
) ([]string, error) {
	if principal.Claims == nil {
		return nil, errors.New("upstream filter: principal claims are absent")
	}
	raw, present := principal.Claims[f.claim]
	if !present {
		return nil, fmt.Errorf("upstream filter: claim %q not present in principal claims", f.claim)
	}
	groups, err := claims.DecodeStringArray(raw)
	if err != nil {
		return nil, fmt.Errorf("upstream filter: claim %q: %w", f.claim, err)
	}

	if len(groups) == 0 {
		return append([]string(nil), f.defaultUpstreams...), nil
	}

	principalGroupSet := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		principalGroupSet[g] = struct{}{}
	}

	effective := make([]string, 0)
	seen := make(map[string]struct{})
	matched := false
	for _, rule := range f.rules {
		if !ruleMatches(rule, principalGroupSet) {
			continue
		}
		matched = true
		for _, name := range rule.UpstreamProviders {
			if _, dup := seen[name]; dup {
				continue
			}
			seen[name] = struct{}{}
			effective = append(effective, name)
		}
	}
	if !matched {
		return append([]string(nil), f.defaultUpstreams...), nil
	}
	return effective, nil
}

// ruleMatches returns true if any of the rule's Groups values is in the
// principal's group set (any-of / OR semantics).
func ruleMatches(rule GroupRule, principalGroupSet map[string]struct{}) bool {
	for _, g := range rule.Groups {
		if _, ok := principalGroupSet[g]; ok {
			return true
		}
	}
	return false
}

func validateRule(rule GroupRule, index int, firstUpstream string, validProviders map[string]struct{}) error {
	if len(rule.Groups) == 0 {
		return fmt.Errorf("rules[%d]: groups must not be empty", index)
	}
	if hasEmpty(rule.Groups) {
		return fmt.Errorf("rules[%d]: groups must not contain empty values", index)
	}
	if hasDuplicates(rule.Groups) {
		return fmt.Errorf("rules[%d]: groups must not contain duplicate values", index)
	}
	if len(rule.UpstreamProviders) == 0 {
		return fmt.Errorf("rules[%d]: upstreamProviders must not be empty", index)
	}
	return validateProviderList(
		fmt.Sprintf("rules[%d].upstreamProviders", index),
		rule.UpstreamProviders,
		firstUpstream,
		validProviders,
	)
}

func validateProviderList(field string, names []string, firstUpstream string, validProviders map[string]struct{}) error {
	if hasEmpty(names) {
		return fmt.Errorf("%s: must not contain empty values", field)
	}
	if hasDuplicates(names) {
		return fmt.Errorf("%s: must not contain duplicate values", field)
	}
	for _, name := range names {
		if name == firstUpstream {
			return fmt.Errorf("%s: must not reference the mandatory first upstream %q", field, firstUpstream)
		}
		if _, ok := validProviders[name]; !ok {
			return fmt.Errorf("%s: %q is not a configured upstream", field, name)
		}
	}
	return nil
}

func hasEmpty(values []string) bool {
	for _, v := range values {
		if v == "" {
			return true
		}
	}
	return false
}

func hasDuplicates(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, dup := seen[v]; dup {
			return true
		}
		seen[v] = struct{}{}
	}
	return false
}

func cloneRules(in []GroupRule) []GroupRule {
	out := make([]GroupRule, len(in))
	for i, r := range in {
		out[i] = GroupRule{
			Groups:            append([]string(nil), r.Groups...),
			UpstreamProviders: append([]string(nil), r.UpstreamProviders...),
		}
	}
	return out
}
