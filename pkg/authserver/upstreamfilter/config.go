// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package upstreamfilter provides UpstreamFilter implementations for the
// embedded authorization server. Filters narrow the multi-upstream authorization
// chain based on the first upstream's resolved identity, so users only walk the
// upstream providers they are entitled to.
//
// This package implements the handlers.UpstreamFilter contract defined in
// pkg/authserver/server/handlers. See that interface's documentation for the
// runtime lifecycle: single invocation per authorization on the first leg's
// callback, principal is read-only, fail-closed on nil/absent claims.
package upstreamfilter

// GroupBasedFilterConfig is the serializable configuration for the group-based
// upstream filter. It is stored in RunConfig and delivered to the vMCP pod via
// the auth-server config ConfigMap.
type GroupBasedFilterConfig struct {
	// Claim is the exact, top-level JWT/OIDC claim name to read group
	// membership from. Defaults to "groups" when empty. The claim value MUST be
	// a JSON array of strings; other shapes (missing, non-array, mixed types)
	// fail the authorization. Nested claims (e.g. Keycloak's realm_access.roles)
	// must be flattened by the identity provider — nested paths are not
	// supported in this implementation.
	Claim string `json:"claim,omitempty" yaml:"claim,omitempty"`

	// Rules assigns groups to upstream provider subsets. A principal matches a
	// rule if any of the principal's group values appears in the rule's Groups
	// list (any-of / OR semantics). Multiple matching rules are combined by
	// union: the effective upstream chain contains every UpstreamProviders
	// value from every matching rule, deduplicated.
	Rules []GroupRule `json:"rules" yaml:"rules"`

	// DefaultUpstreams is used when the claim is a valid empty array or when no
	// rule matches. When empty or omitted, no optional upstreams are added —
	// the mandatory first upstream remains but the authorization walks no
	// further.
	DefaultUpstreams []string `json:"defaultUpstreams,omitempty" yaml:"defaultUpstreams,omitempty"`
}

// GroupRule pairs a set of principal group names with the set of upstream
// provider names matching principals are entitled to walk.
type GroupRule struct {
	// Groups is the set of principal group names this rule matches. Matching
	// is case-sensitive and exact. Empty and duplicate values are rejected at
	// configuration validation time.
	Groups []string `json:"groups" yaml:"groups"`

	// UpstreamProviders lists the non-first upstream provider names to include
	// in the chain when this rule matches. All names must reference a
	// configured non-first upstream. Empty and duplicate values, references to
	// the mandatory first upstream, and references to undeclared upstreams are
	// rejected at configuration validation time.
	UpstreamProviders []string `json:"upstreamProviders" yaml:"upstreamProviders"`
}
