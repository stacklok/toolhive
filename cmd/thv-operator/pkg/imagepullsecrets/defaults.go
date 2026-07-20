// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package imagepullsecrets provides cluster-wide default imagePullSecrets
// that the ToolHive operator applies to every workload it spawns.
//
// The operator parses a comma-separated list of secret names from the
// TOOLHIVE_DEFAULT_IMAGE_PULL_SECRETS environment variable at startup and
// exposes the result as a Defaults value that controllers consume during
// reconciliation.
//
// Defaults are merged with any per-CR imagePullSecrets at workload-construction
// time. See Defaults.Merge for the precedence rule.
package imagepullsecrets

import (
	"os"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// EnvVar is the environment variable name that the operator parses at startup
// to populate cluster-wide default imagePullSecrets.
//
// The value is a comma-separated list of secret names, e.g. "regcred,otherscred".
// Whitespace around entries is tolerated; empty entries are skipped.
const EnvVar = "TOOLHIVE_DEFAULT_IMAGE_PULL_SECRETS"

// Defaults holds the cluster-wide default imagePullSecrets that the operator
// applies to every workload it spawns when the corresponding CR does not
// explicitly override them.
//
// The zero value is a usable empty Defaults: Merge returns the CR-level value
// unchanged. Construct a populated Defaults via LoadDefaultsFromEnv or
// NewDefaults.
type Defaults struct {
	// secrets is the parsed list of default imagePullSecrets, in the order
	// they were specified in the environment variable. The slice is never
	// shared with callers; Merge always returns a fresh slice.
	secrets []corev1.LocalObjectReference
}

// NewDefaults constructs a Defaults from a slice of secret names. Names are
// trimmed of surrounding whitespace; empty names are skipped.
func NewDefaults(names []string) Defaults {
	parsed := make([]corev1.LocalObjectReference, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		parsed = append(parsed, corev1.LocalObjectReference{Name: name})
	}
	return Defaults{secrets: parsed}
}

// LoadDefaultsFromEnv parses Defaults from the
// TOOLHIVE_DEFAULT_IMAGE_PULL_SECRETS environment variable.
//
// The variable is a comma-separated list of secret names. An empty or unset
// variable yields an empty Defaults whose Merge is a no-op.
func LoadDefaultsFromEnv() Defaults {
	return NewDefaults(strings.Split(os.Getenv(EnvVar), ","))
}

// List returns a freshly allocated copy of the configured default
// imagePullSecrets. The caller may freely mutate the returned slice.
// An empty Defaults returns nil (not a zero-length slice) so callers can
// leave a PodSpec or ServiceAccount field unset.
func (d Defaults) List() []corev1.LocalObjectReference {
	if len(d.secrets) == 0 {
		return nil
	}
	return slices.Clone(d.secrets)
}

// Merge combines the cluster-wide defaults with the CR-level imagePullSecrets
// and returns the resulting list.
//
// Precedence rule: chart-level defaults are appended additively to the
// CR-level list, with the CR-level entries taking priority on name conflicts.
// Concretely:
//
//   - The CR-level list comes first in the result, preserving its order.
//   - Each chart-level default is appended only if its Name does not already
//     appear in the CR-level list (deduplication is by Name).
//   - The CR-level list is never mutated; callers receive a fresh slice.
//
// If both inputs are empty, Merge returns nil so callers can leave the
// PodSpec/ServiceAccount field unset.
func (d Defaults) Merge(crLevel []corev1.LocalObjectReference) []corev1.LocalObjectReference {
	if len(crLevel) == 0 && len(d.secrets) == 0 {
		return nil
	}

	merged := make([]corev1.LocalObjectReference, 0, len(crLevel)+len(d.secrets))
	seen := make(map[string]struct{}, len(crLevel)+len(d.secrets))

	for _, ref := range crLevel {
		if _, dup := seen[ref.Name]; dup {
			continue
		}
		seen[ref.Name] = struct{}{}
		merged = append(merged, ref)
	}

	for _, ref := range d.secrets {
		if _, dup := seen[ref.Name]; dup {
			continue
		}
		seen[ref.Name] = struct{}{}
		merged = append(merged, ref)
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}
