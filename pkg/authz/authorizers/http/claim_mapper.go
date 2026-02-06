// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

// ClaimMapper defines the interface for mapping JWT claims to principal attributes.
// Different PDP implementations may have different conventions for claim names
// (e.g., MPE uses m-prefixed claims like "mroles", while OIDC uses standard claims like "roles").
type ClaimMapper interface {
	// MapClaims maps JWT claims to principal attributes suitable for the target PDP.
	// The input is the raw JWT claims map, and the output is a map with claims
	// transformed according to the mapper's conventions.
	MapClaims(claims map[string]any) map[string]any
}

// MPEClaimMapper implements ClaimMapper for Manetu PolicyEngine (MPE).
// MPE uses m-prefixed claims (mroles, mgroups, mclearance, mannotations)
// and also accepts standard OIDC claims (roles, groups, clearance, annotations)
// which are mapped to their m-prefixed equivalents.
type MPEClaimMapper struct{}

// MapClaims maps JWT claims to MPE-compatible principal attributes.
// It maps standard JWT claims to MPE-specific principal attributes:
//   - sub -> sub (subject identifier)
//   - roles/mroles -> mroles (roles)
//   - groups/mgroups -> mgroups (groups)
//   - scope/scopes -> scopes (access scopes)
//   - clearance/mclearance -> mclearance (clearance level)
//   - annotations/mannotations -> mannotations (additional annotations)
//
// Returns map[string]any to ensure the PDP can properly unmarshal
// the PORC structure for identity phase evaluation.
func (*MPEClaimMapper) MapClaims(claims map[string]any) map[string]any {
	principal := make(map[string]any)

	if claims == nil {
		return principal
	}

	// Map standard JWT claims
	if sub, ok := claims["sub"]; ok {
		principal["sub"] = sub
	}

	// Map roles (check both 'roles' and 'mroles')
	if roles, ok := claims["mroles"]; ok {
		principal["mroles"] = roles
	} else if roles, ok := claims["roles"]; ok {
		principal["mroles"] = roles
	}

	// Map groups (check both 'groups' and 'mgroups')
	if groups, ok := claims["mgroups"]; ok {
		principal["mgroups"] = groups
	} else if groups, ok := claims["groups"]; ok {
		principal["mgroups"] = groups
	}

	// Map scopes (check both 'scope' and 'scopes')
	if scopes, ok := claims["scopes"]; ok {
		principal["scopes"] = scopes
	} else if scope, ok := claims["scope"]; ok {
		principal["scopes"] = scope
	}

	// Map clearance level
	if clearance, ok := claims["mclearance"]; ok {
		principal["mclearance"] = clearance
	} else if clearance, ok := claims["clearance"]; ok {
		principal["mclearance"] = clearance
	}

	// Map annotations (initialize empty if not present for identity phase)
	if annotations, ok := claims["mannotations"]; ok {
		principal["mannotations"] = annotations
	} else if annotations, ok := claims["annotations"]; ok {
		principal["mannotations"] = annotations
	} else {
		// Some PDPs require mannotations to be present for identity phase evaluation
		principal["mannotations"] = make(map[string]any)
	}

	return principal
}

// StandardClaimMapper implements ClaimMapper for standard OIDC claims.
// This mapper passes through standard OIDC claim names without modification
// and can be used with PDPs that expect standard OIDC conventions.
type StandardClaimMapper struct{}

// MapClaims maps JWT claims using standard OIDC conventions.
// It preserves standard claim names and normalizes common variations:
//   - sub -> sub (subject identifier)
//   - roles -> roles (roles, preserving standard name)
//   - groups -> groups (groups, preserving standard name)
//   - scope/scopes -> scopes (access scopes, normalized to plural)
func (*StandardClaimMapper) MapClaims(claims map[string]any) map[string]any {
	principal := make(map[string]any)

	if claims == nil {
		return principal
	}

	// Map standard JWT claims
	if sub, ok := claims["sub"]; ok {
		principal["sub"] = sub
	}

	// Map roles (preserve standard name)
	if roles, ok := claims["roles"]; ok {
		principal["roles"] = roles
	}

	// Map groups (preserve standard name)
	if groups, ok := claims["groups"]; ok {
		principal["groups"] = groups
	}

	// Map scopes (normalize to plural form)
	if scopes, ok := claims["scopes"]; ok {
		principal["scopes"] = scopes
	} else if scope, ok := claims["scope"]; ok {
		principal["scopes"] = scope
	}

	return principal
}
