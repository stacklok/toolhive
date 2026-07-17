// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pagination

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

func TestListAll(t *testing.T) {
	t.Parallel()

	errFetch := errors.New("boom")

	tests := []struct {
		name string
		// pages maps the requested cursor to the page it returns.
		pages     map[mcp.Cursor]page
		fetchErr  error
		wantItems []int
		wantErr   bool
	}{
		{
			name: "single page with empty next cursor terminates after one call",
			pages: map[mcp.Cursor]page{
				"": {items: []int{1, 2, 3}, next: ""},
			},
			wantItems: []int{1, 2, 3},
		},
		{
			name: "multiple pages accumulate in order",
			pages: map[mcp.Cursor]page{
				"":   {items: []int{1, 2}, next: "p1"},
				"p1": {items: []int{3, 4}, next: "p2"},
				"p2": {items: []int{5}, next: ""},
			},
			wantItems: []int{1, 2, 3, 4, 5},
		},
		{
			name: "empty result returns no items",
			pages: map[mcp.Cursor]page{
				"": {items: nil, next: ""},
			},
			wantItems: nil,
		},
		{
			name: "repeated cursor aborts with error",
			pages: map[mcp.Cursor]page{
				"":     {items: []int{1}, next: "loop"},
				"loop": {items: []int{2}, next: "loop"},
			},
			wantErr: true,
		},
		{
			name:     "fetch error is propagated",
			fetchErr: errFetch,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fetch := func(_ context.Context, cursor mcp.Cursor) ([]int, mcp.Cursor, error) {
				if tt.fetchErr != nil {
					return nil, "", tt.fetchErr
				}
				p, ok := tt.pages[cursor]
				require.True(t, ok, "unexpected cursor requested: %q", cursor)
				return p.items, p.next, nil
			}

			got, err := ListAll(t.Context(), fetch)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantItems, got)
		})
	}
}

// page is a canned pagination response used by the table test.
type page struct {
	items []int
	next  mcp.Cursor
}
