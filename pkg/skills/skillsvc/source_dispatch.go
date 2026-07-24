// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package skillsvc

import (
	"context"
	"fmt"
	"net/http"

	nameref "github.com/google/go-containerregistry/pkg/name"

	"github.com/stacklok/toolhive-core/httperr"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
)

// sourceOps are the per-kind leaf operations dispatchSource routes a skill
// source to.
type sourceOps[T any] struct {
	// git handles a git:// reference.
	git func(ctx context.Context, gitURL string) (T, error)
	// oci handles a direct OCI reference.
	oci func(ctx context.Context, ref nameref.Reference) (T, error)
	// registry handles a source resolved through the registry catalogue
	// (either the OCI ambiguity fallback or a plain-name lookup).
	registry func(ctx context.Context, resolved *registryResolveResult) (T, error)
	// plainName, when non-nil, handles a validated bare skill name instead
	// of the default registry lookup — for callers with a richer flow
	// (Install checks the local OCI store and takes a per-name lock).
	plainName func(ctx context.Context, name string) (T, error)
}

// dispatchSource is the single source of truth for how a skill source
// string is routed: git prefix first (unambiguous), then OCI-shaped
// references — with a registry-catalogue fallback when a direct OCI
// operation fails for an *ambiguous* "namespace/name" (see
// isUnambiguousOCIRef) — and finally validated plain skill names via the
// registry. Install and upgrade's re-resolution previously each maintained
// their own copy of this sequence, and the two drifted (the upgrade copy
// lost the fallback branch entirely); every consumer must go through this
// function so they cannot diverge again.
//
// Fallback error precedence: when the direct OCI operation fails and the
// registry lookup itself errors, the registry error wins (it is the more
// actionable signal); when the registry simply has no match, the original
// OCI error is returned.
func dispatchSource[T any](ctx context.Context, s *service, source string, ops sourceOps[T]) (T, error) {
	var zero T

	if gitresolver.IsGitReference(source) {
		return ops.git(ctx, source)
	}

	ref, isOCI, err := parseOCIReference(source)
	if err != nil {
		return zero, httperr.WithCode(
			fmt.Errorf("invalid OCI reference %q: %w", source, err),
			http.StatusBadRequest,
		)
	}
	if isOCI {
		result, ociErr := ops.oci(ctx, ref)
		if ociErr == nil {
			return result, nil
		}
		// Direct OCI failed — an ambiguous "namespace/name" may be a
		// registry catalogue name. Names that are unambiguously OCI
		// (digest, explicit tag, or multi-segment path) must not trigger a
		// registry search.
		if isUnambiguousOCIRef(source, ref) {
			return zero, ociErr
		}
		resolved, regErr := s.resolveFromRegistry(source)
		if regErr != nil {
			return zero, regErr
		}
		if resolved == nil {
			return zero, ociErr
		}
		return ops.registry(ctx, resolved)
	}

	if err := skills.ValidateSkillName(source); err != nil {
		return zero, httperr.WithCode(err, http.StatusBadRequest)
	}
	if ops.plainName != nil {
		return ops.plainName(ctx, source)
	}
	resolved, regErr := s.resolveFromRegistry(source)
	if regErr != nil {
		return zero, regErr
	}
	if resolved == nil {
		return zero, httperr.WithCode(
			fmt.Errorf("skill %q not found in registry", source),
			http.StatusNotFound,
		)
	}
	return ops.registry(ctx, resolved)
}
