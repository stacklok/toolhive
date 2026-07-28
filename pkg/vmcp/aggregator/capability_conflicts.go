// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package aggregator

import (
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
//     so renaming is lossless. A name advertised by multiple backends is
//     renamed to "{backendID}_{name}" for EVERY colliding backend (symmetric,
//     mirroring PriorityConflictResolver's prefix fallback); the bare
//     duplicated name is no longer advertised.
//
//     Collision-only renaming trades name stability for minimal change: what
//     a prompt is advertised as depends on what else is deployed. A backend
//     joining with a colliding prompt renames the incumbent out from under a
//     client that pinned the bare name — which then fails prompts/get with
//     not-found, a defined outcome, where it previously silently routed to an
//     arbitrary backend. An in-place backend prompt-set change propagates to
//     live sessions through the list_changed resync path
//     (pkg/vmcp/server/serve_list_changed.go); a backend joining or leaving
//     the group emits no push signal today, so staleness there is bounded by
//     the aggregator cache TTL for routing and by the client's next
//     prompts/list for listing.
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

// resolvePromptConflicts resolves prompt name conflicts across backends.
// A name advertised by exactly one backend passes through unchanged; a name
// advertised by multiple backends is renamed to "{backendID}_{name}" for each
// of them. If a prefixed name still collides (the same backend advertises a
// name twice, or another backend advertises the literal prefixed name), the
// first occurrence in deterministic order wins and later ones are dropped
// with a warning. backendIDs supplies the deterministic (sorted) iteration
// order; the result is sorted by resolved name.
func resolvePromptConflicts(
	backendIDs []string,
	capabilities map[string]*BackendCapabilities,
) []ResolvedPrompt {
	// First pass: count occurrences per name, so a collision renames ALL of
	// its participants (symmetric), not just the ones encountered later.
	nameCounts := make(map[string]int)
	for _, backendID := range backendIDs {
		for _, prompt := range capabilities[backendID].Prompts {
			nameCounts[prompt.Name]++
		}
	}

	seen := make(map[string]string) // resolved name -> winning backend ID
	resolved := make([]ResolvedPrompt, 0, len(nameCounts))
	for _, backendID := range backendIDs {
		for _, prompt := range capabilities[backendID].Prompts {
			resolvedName := prompt.Name
			if nameCounts[prompt.Name] > 1 {
				resolvedName = backendID + "_" + prompt.Name
				slog.Warn("prompt name duplicated across backends, advertising with backend prefix",
					"prompt", prompt.Name, "backend", backendID, "advertised_name", resolvedName)
			}
			if winner, duplicate := seen[resolvedName]; duplicate {
				slog.Warn("prompt name still duplicated after prefixing, dropping later duplicate",
					"advertised_name", resolvedName, "kept_backend", winner, "dropped_backend", backendID)
				continue
			}
			seen[resolvedName] = backendID

			resolvedPrompt := ResolvedPrompt{Prompt: prompt, OriginalName: prompt.Name}
			resolvedPrompt.Name = resolvedName
			resolved = append(resolved, resolvedPrompt)
		}
	}

	sort.Slice(resolved, func(i, j int) bool { return resolved[i].Name < resolved[j].Name })
	return resolved
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
