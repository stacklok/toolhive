// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// This file resolves cross-backend identity conflicts for the list
// capabilities NOT covered by the ConflictResolver strategies: resources
// (identity: URI), resource templates (identity: URITemplate) and prompts
// (identity: Name). Tools are resolved separately through
// ConflictResolver.ResolveToolConflicts.
//
// ResolveConflicts is the ENFORCEMENT POINT for these policies: after it
// runs, every identity is unique within its list. The first-wins guards in
// the merge helpers (mergeResources/mergeResourceTemplates/mergePrompts in
// default_aggregator.go) re-check the same invariant, but only as defence in
// depth for direct MergeCapabilities callers — in the aggregation pipeline
// they can never fire. Policy changes belong here, not there.
//
// The policies deliberately differ per type:
//
//   - Resource URIs and template strings are LOCATORS, not names. The client
//     passes them back verbatim (resources/read, resources/subscribe,
//     completion refs), backends emit them in notifications and embedded
//     resource contents, and a template-matched read forwards the client's
//     concrete URI untranslated (see MergeCapabilities). Rewriting them would
//     break those round trips, so a duplicated URI is instead advertised
//     ONCE: the backend earliest in sorted-backend-ID order wins and later
//     duplicates are dropped with a warning naming the URI and both backends.
//     Reads strictly improve — the routing table keys by URI, so at most one
//     backend was ever served per URI, previously a nondeterministically-
//     chosen one, now a stable one. Listing does regress from N
//     indistinguishable entries to one, but the protocol gives a client no
//     way to address the other N-1, so the extra entries promised reads that
//     could never be honoured.
//
//   - Prompt names are NAMES, like tool names: prompts/get is translated back
//     to the backend's own name via BackendTarget.GetBackendCapabilityName,
//     so renaming is lossless. Every prompt is renamed UNCONDITIONALLY to its
//     backend-prefixed form — the configured prefixFormat applied to the
//     backend ID (default "{workload}_") — mirroring what
//     PrefixConflictResolver does for every tool. The advertised name is
//     therefore a pure function of (backendID, prompt name) and never shifts
//     when unrelated backends join or leave the group. That stability is a
//     security property, not cosmetics: the advertised name is what
//     authorization matches on (Cedar builds Prompt::"<advertised name>"
//     entities — see pkg/vmcp/core/admission.go), so a membership-dependent
//     rename would silently detach permit AND forbid policies from the
//     prompt they were written for, the forbid case failing open.
//
//     Prompt-set changes on a live backend propagate to connected sessions
//     through the list_changed resync path
//     (pkg/vmcp/server/serve_list_changed.go), but that path is add-only for
//     prompts, so a removed prompt stays advertised on already-registered
//     sessions until they reconnect (stacklok/toolhive-core#184). A backend
//     joining or leaving the group emits no push signal today, so staleness
//     there is bounded by the aggregator cache TTL for routing and by the
//     client's next prompts/list for listing.
//
// All functions iterate backends in the caller-supplied deterministic order
// and return key-sorted slices, so the aggregated view (and therefore the
// routing table built from it) is stable run to run.

// resolveResourceConflicts de-duplicates resources by URI across backends.
// backendIDs supplies the deterministic (sorted) iteration order.
func resolveResourceConflicts(
	backendIDs []string,
	capabilities map[string]*BackendCapabilities,
) []vmcp.Resource {
	return dedupeByKey(backendIDs, capabilities, "resource",
		func(caps *BackendCapabilities) []vmcp.Resource { return caps.Resources },
		func(resource vmcp.Resource) string { return resource.URI })
}

// resolveResourceTemplateConflicts de-duplicates resource templates by URI
// template string across backends. backendIDs supplies the deterministic
// (sorted) iteration order.
func resolveResourceTemplateConflicts(
	backendIDs []string,
	capabilities map[string]*BackendCapabilities,
) []vmcp.ResourceTemplate {
	return dedupeByKey(backendIDs, capabilities, "resource_template",
		func(caps *BackendCapabilities) []vmcp.ResourceTemplate { return caps.ResourceTemplates },
		func(template vmcp.ResourceTemplate) string { return template.URITemplate })
}

// resolvePromptConflicts renames EVERY prompt to its backend-prefixed form:
// prefixFormat applied to the backend ID (see applyPrefixFormat), prepended
// to the prompt's own name. Renaming unconditionally keeps the advertised
// name independent of group membership; see the file header for why that
// matters to authorization.
//
// An exact duplicate within one backend (same backend advertises a name
// twice) is dropped with a warning — both entries would advertise and route
// identically, so nothing reachable is lost. Any other residual collision,
// i.e. two distinct (backendID, name) pairs composing to the same prefixed
// string (backend "b1" prompt "x_y" vs backend "b1_x" prompt "y"), is
// ambiguous between backends and returns ErrUnresolvedConflicts rather than
// silently dropping a prompt. backendIDs supplies the deterministic (sorted)
// iteration order; the result is sorted by resolved name.
func resolvePromptConflicts(
	prefixFormat string,
	backendIDs []string,
	capabilities map[string]*BackendCapabilities,
) ([]ResolvedPrompt, error) {
	type advertiser struct{ backendID, originalName string }
	seen := make(map[string]advertiser) // resolved name -> first advertiser
	var resolved []ResolvedPrompt
	for _, backendID := range backendIDs {
		for _, prompt := range capabilities[backendID].Prompts {
			resolvedName := applyPrefixFormat(prefixFormat, backendID, prompt.Name)
			if first, duplicate := seen[resolvedName]; duplicate {
				if first.backendID == backendID {
					slog.Warn("backend advertises the same prompt name twice, dropping later duplicate",
						"backend", backendID, "prompt", prompt.Name)
					continue
				}
				return nil, fmt.Errorf(
					"%w: prompt %q from backend %q and prompt %q from backend %q are both advertised as %q; rename one of them",
					ErrUnresolvedConflicts,
					first.originalName, first.backendID, prompt.Name, backendID, resolvedName)
			}
			seen[resolvedName] = advertiser{backendID: backendID, originalName: prompt.Name}

			resolvedPrompt := ResolvedPrompt{Prompt: prompt, OriginalName: prompt.Name}
			resolvedPrompt.Name = resolvedName
			resolved = append(resolved, resolvedPrompt)
		}
	}

	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Name < resolved[j].Name })
	return resolved, nil
}

// dedupeByKey collects items of one capability kind across backends in the
// given deterministic order, keeping the first item seen for each key and
// dropping later duplicates with a warning. The result is sorted by key.
func dedupeByKey[T any](
	backendIDs []string,
	capabilities map[string]*BackendCapabilities,
	kind string,
	itemsOf func(*BackendCapabilities) []T,
	keyOf func(T) string,
) []T {
	seen := make(map[string]string) // key -> winning backend ID
	var deduped []T
	for _, backendID := range backendIDs {
		for _, item := range itemsOf(capabilities[backendID]) {
			key := keyOf(item)
			if winner, duplicate := seen[key]; duplicate {
				slog.Warn("duplicate capability identity across backends, keeping first in sorted backend order",
					"capability", kind, "key", key, "kept_backend", winner, "dropped_backend", backendID)
				continue
			}
			seen[key] = backendID
			deduped = append(deduped, item)
		}
	}

	sort.Slice(deduped, func(i, j int) bool { return keyOf(deduped[i]) < keyOf(deduped[j]) })
	return deduped
}
