// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package embeddings provides caching for embedding vectors.
package embeddings

import (
	"container/list"
	"sync"
)

// cache implements an LRU cache for embeddings
type cache struct {
	maxSize int
	mu      sync.RWMutex
	items   map[string]*list.Element
	lru     *list.List
	hits    int64
	misses  int64
}

type cacheEntry struct {
	key   string
	value []float32
}

// newCache creates a new LRU cache
func newCache(maxSize int) *cache {
	return &cache{
		maxSize: maxSize,
		items:   make(map[string]*list.Element),
		lru:     list.New(),
	}
}

// Get retrieves an embedding from the cache
func (c *cache) Get(key string) []float32 {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		c.misses++
		return nil
	}

	c.hits++
	c.lru.MoveToFront(elem)
	return elem.Value.(*cacheEntry).value
}

// Put stores an embedding in the cache
func (c *cache) Put(key string, value []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if key already exists
	if elem, ok := c.items[key]; ok {
		c.lru.MoveToFront(elem)
		elem.Value.(*cacheEntry).value = value
		return
	}

	// Add new entry
	entry := &cacheEntry{
		key:   key,
		value: value,
	}
	elem := c.lru.PushFront(entry)
	c.items[key] = elem

	// Evict if necessary
	if c.lru.Len() > c.maxSize {
		c.evict()
	}
}

// evict removes the least recently used item
func (c *cache) evict() {
	elem := c.lru.Back()
	if elem != nil {
		c.lru.Remove(elem)
		entry := elem.Value.(*cacheEntry)
		delete(c.items, entry.key)
	}
}

// Size returns the current cache size
func (c *cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lru.Len()
}

// Clear clears the cache
func (c *cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.items = make(map[string]*list.Element)
	c.lru = list.New()
	c.hits = 0
	c.misses = 0
}
