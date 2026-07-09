// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"slices"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authserver/server/handlers"
)

// StaticUpstreamFilter narrows the authorization chain to a fixed set of
// non-first upstream names, independent of the authenticated principal. It is
// the declarative counterpart of handlers.UpstreamFilter: a deployment that
// knows at configuration time which upstreams must be walked at login (and
// which are acquired on demand) can express that without writing Go.
//
// The handler intersects the returned set with the configured non-first
// upstreams and preserves configured order (see handlers.computeChain), so a
// static keep-set cannot reorder or extend the chain. An empty keep-set
// narrows the chain to just the first (identity) upstream.
type StaticUpstreamFilter struct {
	keep []string
}

// Compile-time check that StaticUpstreamFilter satisfies the handler contract.
var _ handlers.UpstreamFilter = (*StaticUpstreamFilter)(nil)

// NewStaticUpstreamFilter returns a StaticUpstreamFilter that keeps exactly
// the given non-first upstream names in every authorization chain. The slice
// is cloned so later caller mutations cannot change filtering behavior.
func NewStaticUpstreamFilter(keep []string) *StaticUpstreamFilter {
	return &StaticUpstreamFilter{keep: slices.Clone(keep)}
}

// FilterUpstreams implements handlers.UpstreamFilter. The principal and the
// configured set are ignored: the kept subset is fixed at construction time.
// A fresh clone is returned on every call so the handler (or a future caller)
// can never mutate the filter's internal state through the returned slice.
func (f *StaticUpstreamFilter) FilterUpstreams(
	_ context.Context,
	_ auth.PrincipalInfo,
	_ []string,
) ([]string, error) {
	return slices.Clone(f.keep), nil
}
