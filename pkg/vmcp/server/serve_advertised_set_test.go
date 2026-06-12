// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestAdvertisedSetBackendName(t *testing.T) {
	t.Parallel()

	set := &advertisedSet{
		tools:     map[string]string{"echo": "backend-a"},
		resources: map[string]string{"file:///doc": "backend-b"},
	}

	tests := []struct {
		name   string
		method string
		params map[string]any
		want   string
	}{
		{"tool resolves to its backend", "tools/call", map[string]any{"name": "echo"}, "backend-a"},
		{"resource resolves to its backend", "resources/read", map[string]any{"uri": "file:///doc"}, "backend-b"},
		{"unknown tool misses", "tools/call", map[string]any{"name": "nope"}, ""},
		{"unknown resource misses", "resources/read", map[string]any{"uri": "file:///nope"}, ""},
		{"prompts are not labelled on the Serve path", "prompts/get", map[string]any{"name": "echo"}, ""},
		{"unhandled method misses", "tools/list", map[string]any{"name": "echo"}, ""},
		{"non-string tool name misses", "tools/call", map[string]any{"name": 123}, ""},
		{"missing param misses", "tools/call", map[string]any{"other": "x"}, ""},
		{"nil params miss", "tools/call", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, set.backendName(tc.method, tc.params))
		})
	}
}

func TestAdvertisedSetCacheStoreGetEvict(t *testing.T) {
	t.Parallel()

	c, err := newAdvertisedSetCache(8)
	require.NoError(t, err)

	_, ok := c.get("missing")
	assert.False(t, ok, "an unknown session misses")

	set := &advertisedSet{tools: map[string]string{"t": "b"}}
	c.store("s1", set)
	got, ok := c.get("s1")
	require.True(t, ok)
	assert.Equal(t, set, got)

	// store(nil) is a no-op: it must not overwrite or remove the existing entry.
	c.store("s1", nil)
	_, ok = c.get("s1")
	assert.True(t, ok, "store(nil) must not drop an existing entry")

	c.evict("s1")
	_, ok = c.get("s1")
	assert.False(t, ok, "evict removes the entry")

	// Evicting an absent key is a no-op (does not panic).
	c.evict("s1")
}

func TestAdvertisedSetCacheNilReceiverIsSafe(t *testing.T) {
	t.Parallel()

	// The legacy server.New path leaves the cache nil; shared session-cleanup code
	// calls these methods unconditionally, so a nil receiver must not panic.
	var c *advertisedSetCache
	assert.NotPanics(t, func() {
		c.store("s", &advertisedSet{})
		_, ok := c.get("s")
		assert.False(t, ok)
		c.evict("s")
	})
}

func TestNewAdvertisedSetCacheCapacityValidation(t *testing.T) {
	t.Parallel()

	// Capacity 0 falls back to the default rather than silently enabling unbounded growth.
	c, err := newAdvertisedSetCache(0)
	require.NoError(t, err)
	require.NotNil(t, c)
	c.store("s", &advertisedSet{})
	_, ok := c.get("s")
	assert.True(t, ok)

	// A negative capacity is rejected (fail loudly), matching the session manager's
	// validation of the same CacheCapacity field.
	_, err = newAdvertisedSetCache(-1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "capacity must be >= 0")
}

func TestAdvertisedSetCacheIsBounded(t *testing.T) {
	t.Parallel()

	// The cache is the leak backstop for session-end paths the transport is not
	// notified of (DELETE / TTL): once capacity is exceeded, the least-recently-used
	// entry is evicted, so the cache cannot grow without limit.
	const capacity = 4
	c, err := newAdvertisedSetCache(capacity)
	require.NoError(t, err)

	c.store("s0", &advertisedSet{}) // becomes LRU once others are added
	for i := 1; i <= capacity; i++ {
		c.store(string(rune('a'+i)), &advertisedSet{})
	}

	assert.Equal(t, capacity, c.sets.Len(), "the cache must not exceed its capacity")
	_, ok := c.get("s0")
	assert.False(t, ok, "the oldest entry must be evicted once capacity is exceeded")
}

// TestInjectCoreSessionCapabilitiesPopulatesAdvertisedSet proves that session
// registration caches the advertised name→backend mapping (resolving BackendID to the
// registry's display name, with a fallback to the ID for an orphan backend) so audit
// enrichment never re-aggregates (issue #5493).
func TestInjectCoreSessionCapabilitiesPopulatesAdvertisedSet(t *testing.T) {
	t.Parallel()

	fc := &fakeCore{
		tools: []vmcp.Tool{
			{Name: "named-tool", BackendID: "b1"},
			{Name: "orphan-tool", BackendID: "ghost"}, // BackendID absent from the registry
		},
		resources: []vmcp.Resource{{Name: "doc", URI: "file:///doc", BackendID: "b1"}},
	}
	reg := vmcp.NewImmutableRegistry([]vmcp.Backend{{ID: "b1", Name: "backend-one"}})
	cache, err := newAdvertisedSetCache(8)
	require.NoError(t, err)
	srv := &Server{core: fc, backendRegistry: reg, advertisedSets: cache}

	sess := &fakeSDKSession{
		id:        "sess-1",
		tools:     map[string]server.ServerTool{},
		resources: map[string]server.ServerResource{},
	}
	require.NoError(t, srv.injectCoreSessionCapabilities(context.Background(), sess))

	set, ok := cache.get("sess-1")
	require.True(t, ok, "registration must populate the advertised-set cache")
	assert.Equal(t, "backend-one", set.tools["named-tool"], "BackendID must resolve to the registry display name")
	assert.Equal(t, "ghost", set.tools["orphan-tool"], "an unregistered BackendID falls back to the ID")
	assert.Equal(t, "backend-one", set.resources["file:///doc"])

	// The mapping must come from a single aggregation, not a per-request re-aggregation.
	assert.Equal(t, int32(1), fc.listToolsCalls.Load())
	assert.Equal(t, int32(1), fc.listResourcesCalls.Load())
	assert.Zero(t, fc.lookupToolCalls.Load(), "registration must not call core.Lookup*")
}
