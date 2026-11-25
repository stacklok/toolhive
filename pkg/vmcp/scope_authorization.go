// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stacklok/toolhive/pkg/auth"
)

// OAuthScopeSet contains distinct RFC 6749 scope tokens.
// +kubebuilder:validation:MinItems=1
// +kubebuilder:validation:MaxItems=256
// +listType=set
// +kubebuilder:validation:items:Pattern=`^[\x21\x23-\x5B\x5D-\x7E]+$`
type OAuthScopeSet []string

// ScopeAuthorizationConfig adds incoming-token scope requirements to direct tool
// calls. Names are the final advertised names. Cedar authorization still applies;
// missing scopes do not remove otherwise permitted tools from discovery.
type ScopeAuthorizationConfig struct {
	// ClaimName selects the validated incoming claim, defaulting to scope.
	// +optional
	ClaimName string `json:"claimName,omitempty" yaml:"claimName,omitempty"`
	// Tools maps exact advertised tool names to all required OAuth scopes.
	// +kubebuilder:validation:MinProperties=1
	// +kubebuilder:validation:MaxProperties=256
	// +kubebuilder:validation:Required
	Tools map[string]OAuthScopeSet `json:"tools" yaml:"tools"`
	// ScopeImplications declares issuer-defined scope hierarchy, without prefix inference.
	// +kubebuilder:validation:MaxProperties=256
	// +optional
	ScopeImplications map[string]OAuthScopeSet `json:"scopeImplications,omitempty" yaml:"scopeImplications,omitempty"`
}

// InsufficientScopeError identifies an otherwise permitted call needing a larger
// incoming grant. RequiredScopes is the complete operation requirement.
type InsufficientScopeError struct{ RequiredScopes []string }

func (*InsufficientScopeError) Error() string { return "insufficient scope" }

// Unwrap classifies the refusal as authorization failure for non-HTTP consumers.
func (*InsufficientScopeError) Unwrap() error { return ErrAuthorizationFailed }

// DeepCopyInto copies the policy without sharing mutable maps or slices.
func (c *ScopeAuthorizationConfig) DeepCopyInto(out *ScopeAuthorizationConfig) {
	*out = *c
	out.Tools = maps.Clone(c.Tools)
	for k, v := range out.Tools {
		out.Tools[k] = slices.Clone(v)
	}
	out.ScopeImplications = maps.Clone(c.ScopeImplications)
	for k, v := range out.ScopeImplications {
		out.ScopeImplications[k] = slices.Clone(v)
	}
}

// DeepCopy returns an independently owned policy; nil leaves the feature disabled.
func (c *ScopeAuthorizationConfig) DeepCopy() *ScopeAuthorizationConfig {
	if c == nil {
		return nil
	}
	out := new(ScopeAuthorizationConfig)
	c.DeepCopyInto(out)
	return out
}

// Validate rejects malformed configuration and cyclic or unbounded hierarchies.
func (c *ScopeAuthorizationConfig) Validate() error {
	if c == nil {
		return nil
	}
	if c.ClaimName != "" && strings.TrimSpace(c.ClaimName) == "" {
		return fmt.Errorf("claimName must not be blank")
	}
	if len(c.Tools) == 0 || len(c.Tools) > 256 || len(c.ScopeImplications) > 256 {
		return fmt.Errorf("scope authorization requires 1 to 256 tool rules and at most 256 hierarchy entries")
	}
	if err := c.validateTools(); err != nil {
		return err
	}
	return c.validateImplications()
}

// Check evaluates only already-validated incoming claims. Malformed claim values
// fail as ordinary authorization denials, not actionable scope challenges.
func (c *ScopeAuthorizationConfig) Check(identity *auth.Identity, name string) error {
	if c == nil || len(c.Tools[name]) == 0 {
		return nil
	}
	claimName := c.ClaimName
	if claimName == "" {
		claimName = "scope"
	}
	var claim any
	if identity != nil {
		claim = identity.Claims[claimName]
	}
	tokens, err := incomingScopeTokens(claim)
	if err != nil {
		return err
	}
	granted := c.expandScopes(tokens)
	for _, scope := range c.Tools[name] {
		if !granted[scope] {
			required := slices.Clone(c.Tools[name])
			slices.Sort(required)
			return &InsufficientScopeError{RequiredScopes: required}
		}
	}
	return nil
}

func (c *ScopeAuthorizationConfig) validateTools() error {
	for name, scopes := range c.Tools {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("scope authorization tool name must not be empty")
		}
		if err := validateScopeSet(scopes); err != nil {
			return fmt.Errorf("tool %q: %w", name, err)
		}
	}
	return nil
}

func (c *ScopeAuthorizationConfig) validateImplications() error {
	for scope, implied := range c.ScopeImplications {
		if !validScopeToken(scope) {
			return fmt.Errorf("invalid scope hierarchy key")
		}
		if err := validateScopeSet(implied); err != nil {
			return fmt.Errorf("scope hierarchy: %w", err)
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(scope string) bool {
		if visiting[scope] {
			return false
		}
		if visited[scope] {
			return true
		}
		visiting[scope] = true
		for _, child := range c.ScopeImplications[scope] {
			if !visit(child) {
				return false
			}
		}
		visiting[scope], visited[scope] = false, true
		return true
	}
	for scope := range c.ScopeImplications {
		if !visit(scope) {
			return fmt.Errorf("scope hierarchy must not contain cycles")
		}
	}
	return nil
}

func incomingScopeTokens(claim any) ([]string, error) {
	var tokens []string
	switch v := claim.(type) {
	case nil:
	case string:
		tokens = strings.Split(v, " ")
		if v == "" {
			tokens = nil
		}
	case []string:
		tokens = v
	case []any:
		for _, item := range v {
			scope, ok := item.(string)
			if !ok {
				return nil, ErrAuthorizationFailed
			}
			tokens = append(tokens, scope)
		}
	default:
		return nil, ErrAuthorizationFailed
	}
	for _, token := range tokens {
		if !validScopeToken(token) {
			return nil, ErrAuthorizationFailed
		}
	}
	return tokens, nil
}

func (c *ScopeAuthorizationConfig) expandScopes(tokens []string) map[string]bool {
	granted := make(map[string]bool, len(tokens))
	for _, token := range tokens {
		granted[token] = true
	}
	// Expand only configured implications. A visited set bounds traversal even
	// when an embedder bypasses configuration validation.
	queue := slices.Clone(tokens)
	for len(queue) > 0 {
		scope := queue[0]
		queue = queue[1:]
		for _, child := range c.ScopeImplications[scope] {
			if !granted[child] {
				granted[child] = true
				queue = append(queue, child)
			}
		}
	}
	return granted
}

func validateScopeSet(scopes []string) error {
	if len(scopes) == 0 || len(scopes) > 256 {
		return fmt.Errorf("scope sets must contain 1 to 256 tokens")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if !validScopeToken(scope) || seen[scope] {
			return fmt.Errorf("scope sets require distinct valid OAuth scope tokens")
		}
		seen[scope] = true
	}
	return nil
}

func validScopeToken(scope string) bool {
	if scope == "" {
		return false
	}
	for _, c := range scope {
		// RFC 6749 scope-token = %x21 / %x23-5B / %x5D-7E.
		if c < 0x21 || c > 0x7e || c == '"' || c == '\\' {
			return false
		}
	}
	return true
}
