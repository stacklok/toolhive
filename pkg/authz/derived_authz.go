// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

// Some MCP methods name a capability that policy can already talk about, but not
// in a shape MCPMethodToFeatureOperation can express: the feature depends on the
// request body (completion/complete) or a single request names many resources
// (subscriptions/listen). Both used to sit in that map with an empty
// feature/operation pair, which made them always-allowed and let an identity
// denied a prompt or resource reach it anyway through the side door.
//
// These methods are classified here instead. A derived method must NOT also
// appear in MCPMethodToFeatureOperation: one method, one vocabulary, so the
// always-allowed early return can never shadow the checks below
// (TestDerivedMethodsAreNotStaticallyClassified enforces this).

// MCP completion reference discriminators, per the completion/complete schema.
const (
	completionRefTypePrompt   = "ref/prompt"
	completionRefTypeResource = "ref/resource"
)

// maxResourceSubscriptions bounds how many resource URIs one subscriptions/listen
// request may name. Each distinct URI costs one authorization decision, an
// amplification this check introduces: before it, no decision was made at all.
//
// The bound is sized for the SLOWEST supported authorizer, not the fastest.
// Cedar evaluates in-process in microseconds, but the HTTP PDP (authorizers/http)
// issues one outbound POST per decision, sequentially, with no batching or
// caching and a 30s per-call timeout. A caller covered by a wildcard permit
// therefore converts one inbound request into one outbound PDP request per URI.
// A limit tuned to Cedar would be a denial-of-service amplifier against an
// httpv1 deployment.
//
// The limit counts entries as sent, before de-duplication, so it also bounds the
// work of de-duplicating them. Duplicates within the limit collapse to a single
// decision (see subscriptionsListenChecks).
//
// A request over the cap is denied rather than truncated: silently dropping the
// tail would subscribe a caller to less than it asked for while reporting success,
// and would mean the backend registers a different set than was authorized.
const maxResourceSubscriptions = 50

// resourceCheck is one authorization decision: the same feature/operation pair
// the static map carries, bound to the identifier it applies to. featureOperation
// is embedded rather than restated so the two cannot drift apart.
type resourceCheck struct {
	featureOperation
	resourceID string
}

// promptGet and resourceRead name the two decisions every derived method reduces
// to, so a classifier cannot accidentally pair a prompt with a read or authorize
// a resource as a prompt.
func promptGet(name string) resourceCheck {
	return resourceCheck{
		featureOperation: featureOperation{
			Feature:   authorizers.MCPFeaturePrompt,
			Operation: authorizers.MCPOperationGet,
		},
		resourceID: name,
	}
}

func resourceRead(uri string) resourceCheck {
	return resourceCheck{
		featureOperation: featureOperation{
			Feature:   authorizers.MCPFeatureResource,
			Operation: authorizers.MCPOperationRead,
		},
		resourceID: uri,
	}
}

// deriveChecks turns a method's raw params into the decisions that must all pass
// before the request may be dispatched.
//
// Returning an error means "this request has no resolvable authorization target"
// and is always a denial. It is deliberately distinct from returning zero checks,
// which means "this request names no protected capability" and is an allow: an
// error must never be mistaken for an empty check list, or a malformed request
// would sail through as if it had asked for nothing.
type deriveChecks func(params json.RawMessage) ([]resourceCheck, error)

// derivedAuthorizers maps each derived method to its classifier.
var derivedAuthorizers = map[string]deriveChecks{
	"completion/complete":  completionChecks,
	"subscriptions/listen": subscriptionsListenChecks,
}

// completionRef is the ref object identifying what a completion targets. Name and
// URI are pointers so an absent field is distinguishable from an empty one: an
// empty identifier must be rejected, not passed to a policy that might match a
// broad rule on it.
type completionRef struct {
	Type string  `json:"type"`
	Name *string `json:"name"`
	URI  *string `json:"uri"`
}

type completionParams struct {
	Ref *completionRef `json:"ref"`
}

// completionChecks authorizes completion/complete as the capability its ref names:
// a prompt ref needs the same get decision prompts/get needs, a resource-template
// ref the same read decision resources/read needs. Completion candidates are
// derived from the referenced object, so reaching them without that decision
// discloses exactly what the decision exists to withhold.
//
// Both the operation and the identifier are read from the same branch below.
// pkg/mcp's parser falls back from name to uri independently of type, so a ref
// declaring one kind while carrying the other's field would otherwise let the
// authorized identifier and the dispatched one disagree.
func completionChecks(params json.RawMessage) ([]resourceCheck, error) {
	var p completionParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("decoding completion/complete params: %w", err)
	}
	if p.Ref == nil {
		return nil, errors.New("completion/complete: missing ref")
	}
	// No reference kind in the schema carries both fields. One that does is
	// ambiguous about which identifier the backend will act on, so it is refused
	// rather than authorized on the reporter's choice of the two.
	if p.Ref.Name != nil && p.Ref.URI != nil {
		return nil, errors.New("completion/complete: ambiguous ref carries both name and uri")
	}

	switch p.Ref.Type {
	case completionRefTypePrompt:
		if p.Ref.Name == nil || *p.Ref.Name == "" {
			return nil, errors.New("completion/complete: prompt ref without a name")
		}
		return []resourceCheck{promptGet(*p.Ref.Name)}, nil

	case completionRefTypeResource:
		if p.Ref.URI == nil || *p.Ref.URI == "" {
			return nil, errors.New("completion/complete: resource ref without a uri")
		}
		return []resourceCheck{resourceRead(*p.Ref.URI)}, nil

	default:
		// Includes the absent discriminator and pkg/mcp's legacy bare-string ref,
		// neither of which says which capability to authorize.
		// Truncated: ref.type is caller-chosen, and an untruncated %q would let a
		// caller decide how many bytes each denied request writes to the log.
		return nil, fmt.Errorf("completion/complete: unsupported ref type %q", truncateForLog(p.Ref.Type))
	}
}

// notificationSubscriptionFields is the complete subscribable set of the
// 2026-07-28 subscriptions/listen params, mirroring pkg/vmcp/server's
// notificationSubscriptions field-for-field. Only resourceSubscriptions names
// concrete resources; the three list-changed flags are server-wide and carry no
// identifier a policy could match.
//
// The set is enforced, not merely documented: a member outside it is refused
// rather than ignored. Silently ignoring unknown members would make this
// classifier safe only by coincidence of today's schema -- a future revision or
// vendor extension adding another URI-carrying field would reopen the bypass with
// no test failing. Refusing is the same default-deny posture
// MCPMethodToFeatureOperation already takes for unknown methods, and it forces a
// deliberate update here when the schema grows.
const fieldResourceSubscriptions = "resourceSubscriptions"

var notificationSubscriptionFields = map[string]struct{}{
	"toolsListChanged":         {},
	"promptsListChanged":       {},
	"resourcesListChanged":     {},
	fieldResourceSubscriptions: {},
}

// listenParams decodes only far enough to reach the members by name. Both levels
// stay raw on purpose: the member map makes an unrecognised member visible, where
// a typed struct would silently drop it, and the raw notifications value keeps an
// explicit JSON null distinguishable from an absent member.
type listenParams struct {
	Notifications json.RawMessage `json:"notifications"`
}

// errNotAnArray and errNotAnObject describe a member whose JSON type contradicts
// the schema. Such a member is refused rather than coerced: this middleware and
// the backend would each have to guess the same meaning for it, and a guess that
// diverges is how an unauthorized URI reaches a subscription. Absent and empty
// are NOT type violations -- both unambiguously name no resource, so both pass
// with zero checks.
var (
	errNotAnArray  = errors.New("subscriptions/listen: resourceSubscriptions must be an array")
	errNotAnObject = errors.New("subscriptions/listen: notifications must be an object")
)

// isJSONNull reports whether raw is the literal JSON null.
func isJSONNull(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "null"
}

// subscriptionsListenChecks authorizes every URI a subscriptions/listen request
// asks to be notified about, as the same read decision resources/subscribe uses.
// A registered subscription streams notifications/resources/updated for its URI,
// which reveals the resource's existence and change timing even though its
// contents stay behind resources/read.
//
// A denied URI fails the whole request rather than being filtered out of it.
// Filtering would mean rewriting the forwarded body, the acknowledgement, and the
// notification stream, and any of the three drifting reopens the hole; refusing
// the request keeps one decision, made once, in one place.
func subscriptionsListenChecks(params json.RawMessage) ([]resourceCheck, error) {
	var p listenParams
	if err := json.Unmarshal(params, &p); err != nil {
		// A body this middleware cannot decode may still carry a URI the backend
		// decodes and honours, so it cannot be forwarded unchecked.
		return nil, fmt.Errorf("decoding subscriptions/listen params: %w", err)
	}
	if len(p.Notifications) == 0 {
		// The member is absent (e.g. "params":{}). Nothing names a resource, so
		// there is nothing to authorize; the backend rejects the missing required
		// field as invalid params and this middleware does not duplicate its schema
		// validation.
		//
		// A request with NO params member at all does not reach here: Params is nil,
		// the Unmarshal above fails, and the request is denied.
		return nil, nil
	}
	if isJSONNull(p.Notifications) {
		return nil, errNotAnObject
	}

	members := map[string]json.RawMessage{}
	if err := json.Unmarshal(p.Notifications, &members); err != nil {
		return nil, fmt.Errorf("decoding notifications: %w", err)
	}

	for member := range members {
		if _, known := notificationSubscriptionFields[member]; !known {
			return nil, fmt.Errorf("subscriptions/listen: unrecognised notifications member %q",
				truncateForLog(member))
		}
	}

	var uris []string
	if raw, ok := members[fieldResourceSubscriptions]; ok {
		// Decoded through a pointer so an explicit null is distinguishable from an
		// empty array: unmarshalling null straight into a []string yields a nil
		// slice with err == nil, which would read as "no URIs" and pass with zero
		// checks. An empty array leaves a non-nil pointer and is allowed.
		var decoded *[]string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", fieldResourceSubscriptions, err)
		}
		if decoded == nil {
			return nil, errNotAnArray
		}
		uris = *decoded
	}

	if len(uris) > maxResourceSubscriptions {
		return nil, fmt.Errorf("subscriptions/listen: %d resource subscriptions exceeds the limit of %d",
			len(uris), maxResourceSubscriptions)
	}

	// De-duplicated, preserving first-seen order. Repeated URIs yield byte-identical
	// decisions, so asking the authorizer again is pure amplification -- against the
	// HTTP PDP it is one wasted network round trip each. Order is preserved so the
	// decision sequence, and therefore which URI a denial is attributed to, is
	// deterministic.
	//
	// This changes only which decisions are made, never what is forwarded: the
	// request body is proxied verbatim, duplicates included.
	seen := make(map[string]struct{}, len(uris))
	checks := make([]resourceCheck, 0, len(uris))
	for _, uri := range uris {
		if uri == "" {
			return nil, errors.New("subscriptions/listen: empty resource uri")
		}
		if _, duplicate := seen[uri]; duplicate {
			continue
		}
		seen[uri] = struct{}{}
		checks = append(checks, resourceRead(uri))
	}
	return checks, nil
}

// truncateForLog bounds a caller-supplied string before it reaches a log line.
func truncateForLog(s string) string {
	const maxLoggedLen = 64
	if len(s) <= maxLoggedLen {
		return s
	}
	return s[:maxLoggedLen] + "..."
}
