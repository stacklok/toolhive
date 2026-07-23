// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package egressbroker implements the credential-broker data plane for
// untrusted MCP servers (ADR-0001 D3/D5/D6/D7): a per-pod ext_authz service
// that resolves its own pod's identity from downward-API env, enforces
// destination binding (D5) before loading any credential, and returns the
// Authorization header mutation for destinations the EgressPolicy allows.
//
// The broker is the only component in the untrusted pod that ever possesses
// an upstream credential; the backend container never sees one.
package egressbroker

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProviderPolicy is one provider's egress constraint (credential-to-destination
// binding, ADR-0001 D5). This is the runtime mirror of the MCPServer CRD's
// ProviderEgress, rendered into the operator-generated policy ConfigMap.
type ProviderPolicy struct {
	// Provider is the logical upstream provider name (e.g. "github").
	Provider string `yaml:"provider"`
	// AllowedHosts is the exact set of destination hosts the credential may be
	// injected for: exact hostnames or one-label wildcards ("*.example.com").
	AllowedHosts []string `yaml:"allowedHosts"`
	// AllowedMethods constrains the HTTP methods injected on. Empty means
	// GET/HEAD/OPTIONS only (read-only safe default).
	AllowedMethods []string `yaml:"allowedMethods,omitempty"`
	// AllowedPathPrefixes constrains URL paths injected on (prefix match).
	// Empty means all paths on the allowed hosts.
	AllowedPathPrefixes []string `yaml:"allowedPathPrefixes,omitempty"`
}

// policyDocument is the YAML wire format of the policy ConfigMap.
type policyDocument struct {
	Providers []ProviderPolicy `yaml:"providers"`
	// DialAllowlist carries the operator-resolved destination CIDRs for the
	// D7 per-dial IP validation. Rendered by the operator alongside the
	// policy so the sidecar's dial guard and the NetworkPolicy ipBlocks are
	// the same data. Optional in the wire format only so a hand-written
	// policy document can still compile; LoadConfig requires it non-empty
	// (fail closed).
	DialAllowlist []string `yaml:"dialAllowlist,omitempty"`
}

// EgressPolicy is the immutable, startup-compiled lookup the injector
// consults. All matching is case-normalized; the policy is rebuilt only by
// process restart (v1: no hot reload).
type EgressPolicy struct {
	providers     []ProviderPolicy
	dialAllowlist []string
}

// DialAllowlist returns the operator-rendered D7 dial allowlist (may be
// empty when the document predates the field; callers must fail closed).
func (e *EgressPolicy) DialAllowlist() []string {
	return e.dialAllowlist
}

// ParsePolicy compiles the policy ConfigMap document into an immutable
// EgressPolicy. It fails loudly on any malformed or unsafe entry (empty
// provider, empty/invalid host, unknown method, empty path prefix): a
// sidecar that cannot trust its policy must not start (fail closed).
func ParsePolicy(data []byte) (*EgressPolicy, error) {
	var doc policyDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("egressbroker: failed to parse egress policy: %w", err)
	}
	if len(doc.Providers) == 0 {
		return nil, fmt.Errorf("egressbroker: egress policy has no providers")
	}
	providers := make([]ProviderPolicy, 0, len(doc.Providers))
	seen := make(map[string]struct{}, len(doc.Providers))
	for _, p := range doc.Providers {
		norm, err := normalizeProvider(p)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[norm.Provider]; dup {
			return nil, fmt.Errorf("egressbroker: duplicate provider %q in egress policy", norm.Provider)
		}
		seen[norm.Provider] = struct{}{}
		providers = append(providers, norm)
	}
	return &EgressPolicy{providers: providers, dialAllowlist: doc.DialAllowlist}, nil
}

// ProviderNames returns the sorted provider names (used to render the Envoy
// route allowlist deterministically).
func (e *EgressPolicy) ProviderNames() []string {
	names := make([]string, 0, len(e.providers))
	for _, p := range e.providers {
		names = append(names, p.Provider)
	}
	slices.Sort(names)
	return names
}

// ProviderFor returns the provider whose AllowedHosts match host. Host
// matching is exact or one-label wildcard; if multiple providers could match,
// the one with the longest (most specific) matched host pattern wins. This is
// the first half of D5: it must be consulted before any credential load.
func (e *EgressPolicy) ProviderFor(host string) (string, bool) {
	host = normalizeHost(host)
	best := -1
	provider := ""
	for i := range e.providers {
		for _, pattern := range e.providers[i].AllowedHosts {
			if hostMatches(pattern, host) && len(pattern) > best {
				best = len(pattern)
				provider = e.providers[i].Provider
			}
		}
	}
	return provider, best >= 0
}

// Allows reports whether the provider's policy permits method+path (the
// second half of D5, evaluated before any credential load or header write).
// provider must be a name previously returned by ProviderFor.
func (e *EgressPolicy) Allows(provider, method, path string) bool {
	return e.AllowsMethod(provider, method) && e.AllowsPath(provider, path)
}

// AllowsMethod is the method half of Allows (used by the injector to name
// the deny-reason dimension).
func (e *EgressPolicy) AllowsMethod(provider, method string) bool {
	for i := range e.providers {
		if e.providers[i].Provider == provider {
			return methodAllowed(e.providers[i], method)
		}
	}
	return false
}

// AllowsPath is the path half of Allows.
func (e *EgressPolicy) AllowsPath(provider, path string) bool {
	for i := range e.providers {
		if e.providers[i].Provider == provider {
			return pathAllowed(e.providers[i], path)
		}
	}
	return false
}

// HostAllowlist returns the union of all providers' AllowedHosts (used to
// render Envoy route matches; requests to other hosts get no route → denied
// before any credential logic runs).
func (e *EgressPolicy) HostAllowlist() []string {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range e.providers {
		for _, h := range p.AllowedHosts {
			if _, ok := seen[h]; !ok {
				seen[h] = struct{}{}
				out = append(out, h)
			}
		}
	}
	slices.Sort(out)
	return out
}

// normalizeProvider lowercases and validates one policy entry.
func normalizeProvider(p ProviderPolicy) (ProviderPolicy, error) {
	if strings.TrimSpace(p.Provider) == "" {
		return ProviderPolicy{}, fmt.Errorf("egressbroker: provider with empty name in egress policy")
	}
	if len(p.AllowedHosts) == 0 {
		return ProviderPolicy{}, fmt.Errorf("egressbroker: provider %q has no allowedHosts", p.Provider)
	}
	out := ProviderPolicy{
		Provider:            p.Provider,
		AllowedHosts:        make([]string, 0, len(p.AllowedHosts)),
		AllowedMethods:      make([]string, 0, len(p.AllowedMethods)),
		AllowedPathPrefixes: make([]string, 0, len(p.AllowedPathPrefixes)),
	}
	for _, h := range p.AllowedHosts {
		h = normalizeHost(h)
		if err := validateHostPattern(h); err != nil {
			return ProviderPolicy{}, fmt.Errorf("egressbroker: provider %q: %w", p.Provider, err)
		}
		out.AllowedHosts = append(out.AllowedHosts, h)
	}
	for _, m := range p.AllowedMethods {
		m = strings.ToUpper(strings.TrimSpace(m))
		if !knownMethod(m) {
			return ProviderPolicy{}, fmt.Errorf("egressbroker: provider %q has unknown method %q", p.Provider, m)
		}
		out.AllowedMethods = append(out.AllowedMethods, m)
	}
	for _, prefix := range p.AllowedPathPrefixes {
		if !strings.HasPrefix(prefix, "/") {
			return ProviderPolicy{}, fmt.Errorf(
				"egressbroker: provider %q has path prefix %q not starting with '/'", p.Provider, prefix)
		}
		out.AllowedPathPrefixes = append(out.AllowedPathPrefixes, prefix)
	}
	return out, nil
}

// normalizeHost strips a trailing dot and lowercases; host patterns and
// request hosts are always compared in this form.
func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

// validateHostPattern enforces the CRD's host grammar at the data plane too
// (defense-in-depth): hostnames only — no IP literals or CIDRs (the CRD
// grammar forbids them and dial-time IP validation is D7's job, not D5's), no
// ports, no schemes, no path, at most one leading one-label wildcard.
func validateHostPattern(host string) error {
	if host == "" {
		return fmt.Errorf("allowedHosts entry is empty")
	}
	if strings.ContainsAny(host, "/:@ \t") {
		return fmt.Errorf("allowedHosts entry %q must be a bare hostname (no scheme, port, path, or userinfo)", host)
	}
	body := strings.TrimPrefix(host, "*.")
	if body == "" || !strings.Contains(body, ".") {
		return fmt.Errorf("allowedHosts entry %q is not a valid hostname", host)
	}
	if strings.Contains(body, "*") {
		return fmt.Errorf("allowedHosts entry %q: wildcards only allowed as a leading one-label '*.'", host)
	}
	return nil
}

// hostMatches reports whether pattern (exact or one-label "*.suffix") matches
// host. Both must already be normalized. "*.example.com" matches
// "api.example.com" but NOT "example.com" itself (one-label wildcard).
func hostMatches(pattern, host string) bool {
	if suffix, wildcard := strings.CutPrefix(pattern, "*."); wildcard {
		return strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1
	}
	return pattern == host
}

// methodAllowed: empty AllowedMethods defaults to safe read-only methods.
func methodAllowed(p ProviderPolicy, method string) bool {
	method = strings.ToUpper(method)
	if len(p.AllowedMethods) == 0 {
		switch method {
		case "GET", "HEAD", "OPTIONS":
			return true
		}
		return false
	}
	return slices.Contains(p.AllowedMethods, method)
}

// pathAllowed: empty AllowedPathPrefixes means all paths ("/" prefix).
func pathAllowed(p ProviderPolicy, path string) bool {
	if path == "" {
		path = "/"
	}
	if len(p.AllowedPathPrefixes) == 0 {
		return true
	}
	return slices.ContainsFunc(p.AllowedPathPrefixes, func(prefix string) bool {
		return strings.HasPrefix(path, prefix)
	})
}

func knownMethod(m string) bool {
	switch m {
	case "GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}
