// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/upstreamfilter"
)

type contractFilterFunc func(context.Context, upstreamfilter.FilterPrincipal, []string) ([]string, error)

func (f contractFilterFunc) FilterUpstreams(
	ctx context.Context, principal upstreamfilter.FilterPrincipal, configured []string,
) ([]string, error) {
	return f(ctx, principal, configured)
}

// Assignments in both directions pin type identity, not just structural similarity.
var (
	_ UpstreamFilter                                       = contractFilterFunc(nil)
	_ upstreamfilter.UpstreamFilter                        = (*stubUpstreamFilter)(nil)
	_ func(UpstreamFilter) Option                          = WithUpstreamFilter
	_ func(upstreamfilter.UpstreamFilter) Option           = WithUpstreamFilter
	_ func(FilterPrincipal) upstreamfilter.FilterPrincipal = func(p upstreamfilter.FilterPrincipal) FilterPrincipal { return p }
)

func TestUpstreamFilterCompatibility(t *testing.T) {
	t.Parallel()

	boom := errors.New("filter failed")
	for _, fail := range []bool{false, true} {
		for _, legacy := range []bool{false, true} {
			name := "new signature"
			if legacy {
				name = "legacy signature"
			}
			if fail {
				name += "/error"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				resultErr := error(nil)
				if fail {
					resultErr = boom
				}
				old := &stubUpstreamFilter{keep: []string{"third", "unknown", "first", "second"}, err: resultErr}
				var filter upstreamfilter.UpstreamFilter = contractFilterFunc(old.FilterUpstreams)
				if legacy {
					var alias UpstreamFilter = old
					filter = alias
				}
				h := newChainTestHandler(t, []string{"first", "second", "third"}, WithUpstreamFilter(filter))
				var principal FilterPrincipal = filterPrincipal{subject: "mapped-subject", canonicalUserID: "storage-user"}
				chain, err := h.computeChain(t.Context(), principal)
				if fail {
					require.ErrorIs(t, err, boom)
					assert.Nil(t, chain)
				} else {
					require.NoError(t, err)
					assert.Equal(t, []string{"first", "second", "third"}, chain)
				}
				assert.Equal(t, 1, old.calls)
				assert.Equal(t, []string{"second", "third"}, old.capturedArgs)
				assert.Equal(t, principal, old.capturedPrincipal)
				assert.Nil(t, old.capturedPrincipal.GetClaims())
			})
		}
	}
}
