// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import "slices"

// DefaultMaxDelegationDepth is the default cap on how many nested RFC 8693
// "act" entries ParseDelegationChain will follow. It is aligned with the
// issuance-side cap in pkg/authserver/server/tokenexchange/handler.go
// (maxDelegationDepth) and the claim-nesting cap in
// pkg/authz/authorizers/cedar/entity.go (maxClaimNestingDepth), both 10.
const DefaultMaxDelegationDepth = 10

// DelegatedActor represents one acting party in an RFC 8693 §4.1 delegation
// chain (one "act" claim value).
type DelegatedActor struct {
	// Subject identifies the acting party (the "sub" member of the act claim).
	// Empty both when the act claim carries no "sub" and when "sub" is present
	// but not a string; the latter case is distinguishable from the former by
	// its raw value surfacing under Claims instead of vanishing.
	Subject string `json:"sub,omitempty"`

	// Claims holds the remaining members of the act claim (e.g. "iss",
	// "client_id"), preserving forward-compatibility with act claims that
	// carry more than a subject. The "act_claims" tag disambiguates these
	// per-actor members from the identity's top-level claims. A non-string
	// "sub" is included here rather than dropped, so a malformed subject is
	// still visible. Internal claims (see internalClaims in context.go) are
	// filtered out the same as they are from top-level Identity.Claims.
	Claims map[string]any `json:"act_claims,omitempty"`
}

// DelegationChain is the parsed form of a JWT's RFC 8693 "act" claim: the
// ordered list of parties that acted on behalf of the token subject.
type DelegationChain struct {
	// Actors are the acting parties, outermost (most recent) first: Actors[0]
	// is the direct delegate that presented the token, Actors[len-1] the
	// earliest actor in the chain.
	Actors []DelegatedActor `json:"actors"`

	// Truncated is true when the chain was deeper than the configured maximum
	// depth and trailing actors were dropped.
	Truncated bool `json:"truncated"`

	// DroppedCount reports how many actors were dropped due to the depth cap.
	// Zero (and omitted from JSON) when nothing was dropped.
	DroppedCount int `json:"dropped_count,omitempty"`

	// Malformed is true when a present, non-nil "act" value (top-level or
	// nested) was not a JSON object, per RFC 8693 §4.1. This is distinct from
	// a legitimately absent/null "act": that case leaves Malformed false, with
	// or without actors already parsed from shallower levels of the chain.
	Malformed bool `json:"malformed,omitempty"`
}

// ParseDelegationChain parses a JWT "act" claim value into a DelegationChain.
// The act value is expected to be a map (as produced by JSON decoding); any
// other non-nil shape yields Malformed=true, with whatever actors were parsed
// from shallower levels preserved. Nested "act" members are followed up to
// maxDepth actors; maxDepth <= 0 uses DefaultMaxDelegationDepth. A nil/absent
// act value yields the zero chain (no actors, not truncated, not malformed).
func ParseDelegationChain(act any, maxDepth int) DelegationChain {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxDelegationDepth
	}

	chain := DelegationChain{Actors: []DelegatedActor{}}
	current := act
	for len(chain.Actors) < maxDepth {
		m, ok := current.(map[string]any)
		if !ok {
			// A nil current here means act was absent/null (either at the
			// top level, or the legitimate end of a chain checked below) —
			// not malformed. Anything else present but not an object is.
			if current != nil {
				chain.Malformed = true
			}
			return chain
		}

		actor := DelegatedActor{}
		sub, subIsString := m["sub"].(string)
		if subIsString {
			actor.Subject = sub
		}
		claims := make(map[string]any, len(m))
		for k, v := range m {
			if k == "act" || (k == "sub" && subIsString) || slices.Contains(internalClaims, k) {
				continue
			}
			claims[k] = v
		}
		if len(claims) > 0 {
			actor.Claims = claims
		}
		chain.Actors = append(chain.Actors, actor)

		next, ok := m["act"]
		if !ok || next == nil {
			return chain
		}
		current = next
	}

	// The depth cap was reached. current now holds the last appended actor's
	// nested "act" value: each further map entry is a dropped actor, so count
	// them and mark the chain truncated when any remain.
	for {
		m, ok := current.(map[string]any)
		if !ok {
			// current is guaranteed non-nil here (the loop above only
			// advances current to a checked, present, non-nil "act" value),
			// so a failed assertion means unparseable data beyond the cap.
			chain.Malformed = true
			break
		}
		chain.Truncated = true
		chain.DroppedCount++
		next, ok := m["act"]
		if !ok || next == nil {
			break
		}
		current = next
	}

	return chain
}
