// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

// unionScopes returns the union of requested and baseline scopes, preserving
// the order of requested first, then appending any baseline scopes not already
// present. Duplicates are removed. Returns nil when the result is empty.
//
// This is used by the DCR registration handler to inject an
// operator-configured scope baseline into the registered client's scope set:
// if a client narrows the scope field at /oauth/register, the baseline scopes
// are still part of the client's registered set so that the client can
// request them later at /oauth/authorize without invalid_scope rejection.
//
// Both inputs must already be validated by the caller (e.g. via ValidateScopes
// for client-supplied scopes). unionScopes does not filter empty strings or
// validate scope syntax — it only deduplicates and merges in stable order.
func unionScopes(requested, baseline []string) []string {
	seen := make(map[string]bool, len(requested)+len(baseline))
	out := make([]string, 0, len(requested)+len(baseline))
	for _, s := range requested {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, s := range baseline {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
