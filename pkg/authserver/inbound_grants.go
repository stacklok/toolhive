// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"fmt"
	"slices"

	tx "github.com/stacklok/toolhive/pkg/authserver/server/tokenexchange"
)

// TokenExchangeInboundGrantRunConfig configures RFC 8693 inbound clients and issuer policies.
type TokenExchangeInboundGrantRunConfig struct {
	DelegateClients []DelegateClientRunConfig            `json:"delegate_clients,omitempty" yaml:"delegate_clients,omitempty"`
	IssuerPolicies  []TokenExchangeIssuerPolicyRunConfig `json:"issuer_policies,omitempty" yaml:"issuer_policies,omitempty"`
}

// TokenExchangeIssuerPolicyRunConfig binds RFC 8693 policy to a trusted issuer declaration.
type TokenExchangeIssuerPolicyRunConfig struct {
	IssuerRef              string   `json:"issuer_ref" yaml:"issuer_ref"`
	ExpectedAudience       string   `json:"expected_audience" yaml:"expected_audience"`
	ActorClaim             string   `json:"actor_claim,omitempty" yaml:"actor_claim,omitempty"`
	AllowedActors          []string `json:"allowed_actors,omitempty" yaml:"allowed_actors,omitempty"`
	ActorMatcher           string   `json:"actor_matcher,omitempty" yaml:"actor_matcher,omitempty"`
	AllowedDelegateClients []string `json:"allowed_delegate_clients" yaml:"allowed_delegate_clients"`
	AllowMayAct            bool     `json:"allow_may_act,omitempty" yaml:"allow_may_act,omitempty"`
}

// JWTBearerInboundGrantRunConfig configures RFC 7523 issuer policies.
type JWTBearerInboundGrantRunConfig struct {
	IssuerPolicies []JWTBearerIssuerPolicyRunConfig `json:"issuer_policies,omitempty" yaml:"issuer_policies,omitempty"`
}

// JWTBearerIssuerPolicyRunConfig binds RFC 7523 policy to a trusted issuer declaration.
type JWTBearerIssuerPolicyRunConfig struct {
	IssuerRef         string                       `json:"issuer_ref" yaml:"issuer_ref"`
	MaxAssertionAge   string                       `json:"max_assertion_age" yaml:"max_assertion_age"`
	SubjectBindings   []tx.JWTBearerSubjectBinding `json:"subject_bindings" yaml:"subject_bindings"`
	AcceptedAudiences []string                     `json:"accepted_audiences,omitempty" yaml:"accepted_audiences,omitempty"`
}

// InboundGrantCapabilities reports the effective grant families after normalization.
type InboundGrantCapabilities struct {
	TokenExchange bool
	JWTBearer     bool
}

// DeprecatedFieldPath identifies a populated legacy field and its canonical replacement.
type DeprecatedFieldPath struct {
	Path        string
	Replacement string
}

// NormalizedInboundGrants contains copied effective inputs consumed by existing runtime validators and registration.
type NormalizedInboundGrants struct {
	DelegateClients  []DelegateClientRunConfig
	TrustedIssuers   []tx.TrustedIssuer
	Capabilities     InboundGrantCapabilities
	DeprecatedFields []DeprecatedFieldPath
}

// NormalizeInboundGrants converts legacy and canonical declarations to the existing effective runtime structures.
// It never mutates cfg or any nested caller-owned slice.
func NormalizeInboundGrants(cfg *RunConfig) (*NormalizedInboundGrants, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}

	result := &NormalizedInboundGrants{
		DelegateClients: cloneDelegateClients(cfg.DelegateClients),
		TrustedIssuers:  cloneTrustedIssuers(cfg.TrustedIssuers),
		Capabilities: InboundGrantCapabilities{
			TokenExchange: cfg.InboundGrants == nil,
		},
	}
	issuerByName, err := indexTrustedIssuers(result.TrustedIssuers)
	if err != nil {
		return nil, err
	}

	legacyTokenExchange := len(cfg.DelegateClients) > 0
	for i := range result.TrustedIssuers {
		issuer := &result.TrustedIssuers[i]
		if hasLegacyTokenExchangePolicy(*issuer) {
			legacyTokenExchange = true
			result.DeprecatedFields = append(result.DeprecatedFields, DeprecatedFieldPath{
				Path: fmt.Sprintf("trusted_issuers[%d]", i), Replacement: "inbound_grants.token_exchange.issuer_policies",
			})
		}
		if issuer.JWTBearerGrant != nil {
			result.Capabilities.JWTBearer = true
			result.DeprecatedFields = append(result.DeprecatedFields, DeprecatedFieldPath{
				Path: fmt.Sprintf("trusted_issuers[%d].jwt_bearer_grant", i), Replacement: "inbound_grants.jwt_bearer.issuer_policies",
			})
		}
	}
	if len(cfg.DelegateClients) > 0 {
		result.DeprecatedFields = append(result.DeprecatedFields, DeprecatedFieldPath{
			Path: "delegate_clients", Replacement: "inbound_grants.token_exchange.delegate_clients",
		})
	}
	if cfg.InboundGrants == nil {
		return result, nil
	}

	if cfg.InboundGrants.TokenExchange != nil {
		if legacyTokenExchange {
			return nil, fmt.Errorf(
				"inbound_grants.token_exchange conflicts with legacy delegate_clients " +
					"or RFC 8693 policy in trusted_issuers",
			)
		}
		result.Capabilities.TokenExchange = true
		result.DelegateClients = cloneDelegateClients(cfg.InboundGrants.TokenExchange.DelegateClients)
		if err := applyTokenExchangePolicies(result.TrustedIssuers, issuerByName,
			cfg.InboundGrants.TokenExchange.IssuerPolicies); err != nil {
			return nil, err
		}
	} else {
		result.Capabilities.TokenExchange = legacyTokenExchange
	}
	// SPIFFE client-auth associations always require the token-exchange grant
	// (SPIFFEGrantTypeTokenExchange is the only grant type they may declare —
	// see validateSPIFFEGrants), independent of the legacy/canonical
	// token-exchange projection above. A SPIFFE-only configuration must not
	// leave Capabilities.TokenExchange false: that would disable the RFC 8693
	// grant handler server-wide and reject every SPIFFE client's own token
	// requests before authentication is even checked.
	if len(cfg.InboundGrants.SPIFFEClientAuth) > 0 {
		result.Capabilities.TokenExchange = true
	}

	if cfg.InboundGrants.JWTBearer != nil {
		if result.Capabilities.JWTBearer {
			return nil, fmt.Errorf("inbound_grants.jwt_bearer conflicts with legacy trusted_issuers[*].jwt_bearer_grant")
		}
		result.Capabilities.JWTBearer = true
		if err := applyJWTBearerPolicies(result.TrustedIssuers, issuerByName,
			cfg.InboundGrants.JWTBearer.IssuerPolicies); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func indexTrustedIssuers(issuers []tx.TrustedIssuer) (map[string]int, error) {
	byName := make(map[string]int, len(issuers))
	byURL := make(map[string]int, len(issuers))
	for i, issuer := range issuers {
		if previous, ok := byURL[issuer.IssuerURL]; ok {
			return nil, fmt.Errorf(
				"trusted_issuers[%d].issuer_url duplicates trusted_issuers[%d].issuer_url %q (configured more than once)",
				i,
				previous,
				issuer.IssuerURL,
			)
		}
		byURL[issuer.IssuerURL] = i
		if issuer.Name == "" {
			continue
		}
		if previous, ok := byName[issuer.Name]; ok {
			return nil, fmt.Errorf("trusted_issuers[%d].name duplicates trusted_issuers[%d].name %q", i, previous, issuer.Name)
		}
		byName[issuer.Name] = i
	}
	return byName, nil
}

func applyTokenExchangePolicies(
	issuers []tx.TrustedIssuer, byName map[string]int, policies []TokenExchangeIssuerPolicyRunConfig,
) error {
	seen := make(map[string]int, len(policies))
	for i, policy := range policies {
		issuerIndex, err := resolveIssuerRef(byName, seen, policy.IssuerRef,
			fmt.Sprintf("inbound_grants.token_exchange.issuer_policies[%d]", i))
		if err != nil {
			return err
		}
		seen[policy.IssuerRef] = i
		issuer := &issuers[issuerIndex]
		issuer.ExpectedAudience = policy.ExpectedAudience
		issuer.ActorClaim = policy.ActorClaim
		issuer.AllowedActors = slices.Clone(policy.AllowedActors)
		issuer.ActorMatcher = policy.ActorMatcher
		issuer.AllowedDelegateClients = slices.Clone(policy.AllowedDelegateClients)
		issuer.AllowMayAct = policy.AllowMayAct
	}
	return nil
}

func applyJWTBearerPolicies(
	issuers []tx.TrustedIssuer, byName map[string]int, policies []JWTBearerIssuerPolicyRunConfig,
) error {
	seen := make(map[string]int, len(policies))
	for i, policy := range policies {
		issuerIndex, err := resolveIssuerRef(byName, seen, policy.IssuerRef,
			fmt.Sprintf("inbound_grants.jwt_bearer.issuer_policies[%d]", i))
		if err != nil {
			return err
		}
		seen[policy.IssuerRef] = i
		issuers[issuerIndex].JWTBearerGrant = &tx.JWTBearerGrantPolicy{
			MaxAssertionAge:   policy.MaxAssertionAge,
			SubjectBindings:   cloneSubjectBindings(policy.SubjectBindings),
			AcceptedAudiences: slices.Clone(policy.AcceptedAudiences),
		}
	}
	return nil
}

func resolveIssuerRef(byName map[string]int, seen map[string]int, ref, path string) (int, error) {
	if ref == "" {
		return 0, fmt.Errorf("%s.issuer_ref is required", path)
	}
	if previous, ok := seen[ref]; ok {
		return 0, fmt.Errorf("%s.issuer_ref duplicates issuer policy [%d] for %q", path, previous, ref)
	}
	index, ok := byName[ref]
	if !ok {
		return 0, fmt.Errorf("%s.issuer_ref references unknown or unnamed trusted issuer %q", path, ref)
	}
	return index, nil
}

func hasLegacyTokenExchangePolicy(issuer tx.TrustedIssuer) bool {
	return issuer.ExpectedAudience != "" || issuer.ActorClaim != "" || len(issuer.AllowedActors) > 0 ||
		issuer.ActorMatcher != "" || len(issuer.AllowedDelegateClients) > 0 || issuer.AllowMayAct
}

func cloneDelegateClients(clients []DelegateClientRunConfig) []DelegateClientRunConfig {
	cloned := slices.Clone(clients)
	for i := range cloned {
		cloned[i].Scopes = slices.Clone(cloned[i].Scopes)
		cloned[i].Audiences = slices.Clone(cloned[i].Audiences)
	}
	return cloned
}

func cloneTrustedIssuers(issuers []tx.TrustedIssuer) []tx.TrustedIssuer {
	cloned := slices.Clone(issuers)
	for i := range cloned {
		cloned[i].AllowedActors = slices.Clone(cloned[i].AllowedActors)
		cloned[i].AllowedDelegateClients = slices.Clone(cloned[i].AllowedDelegateClients)
		if cloned[i].JWTBearerGrant != nil {
			policy := *cloned[i].JWTBearerGrant
			policy.SubjectBindings = cloneSubjectBindings(policy.SubjectBindings)
			policy.AcceptedAudiences = slices.Clone(policy.AcceptedAudiences)
			cloned[i].JWTBearerGrant = &policy
		}
	}
	return cloned
}

func cloneSubjectBindings(bindings []tx.JWTBearerSubjectBinding) []tx.JWTBearerSubjectBinding {
	cloned := slices.Clone(bindings)
	for i := range cloned {
		cloned[i].AllowedResources = slices.Clone(cloned[i].AllowedResources)
	}
	return cloned
}
