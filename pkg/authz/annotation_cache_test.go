// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
)

func boolPtr(b bool) *bool { return &b }

func TestAnnotationCache_Get(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func(*AnnotationCache)
		toolName string
		want     *authorizers.ToolAnnotations
	}{
		{
			name:     "returns nil for unknown tool",
			setup:    func(_ *AnnotationCache) {},
			toolName: "nonexistent",
			want:     nil,
		},
		{
			name: "returns stored annotations",
			setup: func(c *AnnotationCache) {
				c.Set("weather", &authorizers.ToolAnnotations{
					ReadOnlyHint: boolPtr(true),
				})
			},
			toolName: "weather",
			want: &authorizers.ToolAnnotations{
				ReadOnlyHint: boolPtr(true),
			},
		},
		{
			name: "returns nil annotations when explicitly stored as nil",
			setup: func(c *AnnotationCache) {
				c.Set("tool-with-nil", nil)
			},
			toolName: "tool-with-nil",
			want:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cache := NewAnnotationCache()
			tc.setup(cache)

			got := cache.Get(tc.toolName)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAnnotationCache_SetAndGet(t *testing.T) {
	t.Parallel()

	cache := NewAnnotationCache()
	ann := &authorizers.ToolAnnotations{
		ReadOnlyHint:    boolPtr(true),
		DestructiveHint: boolPtr(false),
		IdempotentHint:  boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	}

	cache.Set("calculator", ann)
	got := cache.Get("calculator")

	require.NotNil(t, got)
	assert.Equal(t, ann, got)
}

func TestAnnotationCache_SetFromToolsList(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tools         []mcp.Tool
		expectCached  map[string]*authorizers.ToolAnnotations
		expectMissing []string
	}{
		{
			name: "caches tools with annotation hints",
			tools: []mcp.Tool{
				{
					Name: "weather",
					Annotations: mcp.ToolAnnotation{
						ReadOnlyHint: boolPtr(true),
					},
				},
				{
					Name: "deploy",
					Annotations: mcp.ToolAnnotation{
						DestructiveHint: boolPtr(true),
						OpenWorldHint:   boolPtr(true),
					},
				},
			},
			expectCached: map[string]*authorizers.ToolAnnotations{
				"weather": {
					ReadOnlyHint: boolPtr(true),
				},
				"deploy": {
					DestructiveHint: boolPtr(true),
					OpenWorldHint:   boolPtr(true),
				},
			},
		},
		{
			name: "skips tools with no annotation hints",
			tools: []mcp.Tool{
				{
					Name:        "no-hints",
					Annotations: mcp.ToolAnnotation{},
				},
				{
					Name: "has-hints",
					Annotations: mcp.ToolAnnotation{
						IdempotentHint: boolPtr(false),
					},
				},
			},
			expectCached: map[string]*authorizers.ToolAnnotations{
				"has-hints": {
					IdempotentHint: boolPtr(false),
				},
			},
			expectMissing: []string{"no-hints"},
		},
		{
			name: "skips tools with only title (no hint fields)",
			tools: []mcp.Tool{
				{
					Name: "titled-only",
					Annotations: mcp.ToolAnnotation{
						Title: "A Tool With Title Only",
					},
				},
			},
			expectCached:  map[string]*authorizers.ToolAnnotations{},
			expectMissing: []string{"titled-only"},
		},
		{
			name:          "handles empty tools list",
			tools:         []mcp.Tool{},
			expectCached:  map[string]*authorizers.ToolAnnotations{},
			expectMissing: []string{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cache := NewAnnotationCache()
			cache.SetFromToolsList(tc.tools)

			for name, wantAnn := range tc.expectCached {
				got := cache.Get(name)
				require.NotNil(t, got, "expected cache hit for tool %q", name)
				assert.Equal(t, wantAnn, got, "annotations mismatch for tool %q", name)
			}

			for _, name := range tc.expectMissing {
				got := cache.Get(name)
				assert.Nil(t, got, "expected cache miss for tool %q", name)
			}
		})
	}
}

func TestAnnotationCache_NilSafety(t *testing.T) {
	t.Parallel()

	var cache *AnnotationCache

	// All methods should be safe to call on a nil cache.
	assert.Nil(t, cache.Get("anything"))

	// Set should not panic.
	assert.NotPanics(t, func() {
		cache.Set("tool", &authorizers.ToolAnnotations{ReadOnlyHint: boolPtr(true)})
	})

	// SetFromToolsList should not panic.
	assert.NotPanics(t, func() {
		cache.SetFromToolsList([]mcp.Tool{
			{Name: "weather", Annotations: mcp.ToolAnnotation{ReadOnlyHint: boolPtr(true)}},
		})
	})
}

func TestAnnotationCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := NewAnnotationCache()
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Half the goroutines write, half read.
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			ann := &authorizers.ToolAnnotations{
				ReadOnlyHint: boolPtr(idx%2 == 0),
			}
			cache.Set("shared-tool", ann)
		}(i)

		go func() {
			defer wg.Done()
			// Get may return nil or a valid pointer; we just need no data race.
			_ = cache.Get("shared-tool")
		}()
	}

	wg.Wait()

	// After all goroutines finish, the cache should still be functional.
	got := cache.Get("shared-tool")
	require.NotNil(t, got, "cache should contain the tool after concurrent writes")
	assert.NotNil(t, got.ReadOnlyHint, "ReadOnlyHint should be set")
}

func TestAnnotationCache_SetFromToolsListEvictsStaleEntries(t *testing.T) {
	t.Parallel()

	cache := NewAnnotationCache()

	// First tools/list: two tools with annotations.
	cache.SetFromToolsList([]mcp.Tool{
		{Name: "weather", Annotations: mcp.ToolAnnotation{ReadOnlyHint: boolPtr(true)}},
		{Name: "deploy", Annotations: mcp.ToolAnnotation{DestructiveHint: boolPtr(true)}},
	})

	require.NotNil(t, cache.Get("weather"))
	require.NotNil(t, cache.Get("deploy"))

	// Second tools/list: "deploy" is gone, only "weather" remains.
	cache.SetFromToolsList([]mcp.Tool{
		{Name: "weather", Annotations: mcp.ToolAnnotation{ReadOnlyHint: boolPtr(true)}},
	})

	assert.NotNil(t, cache.Get("weather"), "weather should still be cached")
	assert.Nil(t, cache.Get("deploy"), "deploy should be evicted after second SetFromToolsList")
}

func TestAnnotationCache_SetOverwritesPrevious(t *testing.T) {
	t.Parallel()

	cache := NewAnnotationCache()

	cache.Set("tool", &authorizers.ToolAnnotations{
		ReadOnlyHint: boolPtr(true),
	})
	cache.Set("tool", &authorizers.ToolAnnotations{
		ReadOnlyHint:    boolPtr(false),
		DestructiveHint: boolPtr(true),
	})

	got := cache.Get("tool")
	require.NotNil(t, got)
	assert.Equal(t, boolPtr(false), got.ReadOnlyHint)
	assert.Equal(t, boolPtr(true), got.DestructiveHint)
}
