// Package awssts provides AWS STS token exchange and SigV4 signing functionality.
package awssts

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const (
	// DefaultCacheSize is the default maximum number of entries in the credential cache.
	DefaultCacheSize = 1000

	// RefreshBuffer is the duration before expiry when credentials should be refreshed.
	RefreshBuffer = 5 * time.Minute
)

// CredentialCache provides thread-safe caching of AWS credentials with LRU eviction.
//
// Cache keys are constructed from role ARN and a SHA-256 hash of the identity token,
// ensuring that:
//   - Different users get different cache entries
//   - Same user with same role gets cached credentials
//   - Token rotation naturally invalidates stale entries
//
// The cache uses LRU eviction when the maximum size is reached.
type CredentialCache struct {
	mu      sync.RWMutex
	cache   map[string]*cacheEntry
	lru     *list.List // Doubly-linked list for LRU ordering
	maxSize int
}

// cacheEntry holds cached credentials and LRU tracking.
type cacheEntry struct {
	key         string
	credentials *Credentials
	element     *list.Element // Pointer to position in LRU list
}

// NewCredentialCache creates a new credential cache with the specified maximum size.
//
// If maxSize is 0 or negative, DefaultCacheSize is used.
func NewCredentialCache(maxSize int) *CredentialCache {
	if maxSize <= 0 {
		maxSize = DefaultCacheSize
	}
	return &CredentialCache{
		cache:   make(map[string]*cacheEntry),
		lru:     list.New(),
		maxSize: maxSize,
	}
}

// Get retrieves cached credentials for the given role ARN and identity token.
//
// Returns nil if:
//   - No cached entry exists
//   - Cached credentials are expired
//   - Cached credentials should be refreshed (within RefreshBuffer of expiry)
//
// On successful retrieval, the entry is moved to the front of the LRU list.
func (c *CredentialCache) Get(roleArn, identityToken string) *Credentials {
	key := buildCacheKey(roleArn, identityToken)

	c.mu.RLock()
	entry, exists := c.cache[key]
	c.mu.RUnlock()

	if !exists {
		return nil
	}

	// Check if credentials should be refreshed
	if entry.credentials.ShouldRefresh() {
		return nil
	}

	// Move to front of LRU list (requires write lock)
	c.mu.Lock()
	c.lru.MoveToFront(entry.element)
	c.mu.Unlock()

	return entry.credentials
}

// Set stores credentials in the cache for the given role ARN and identity token.
//
// If the cache is at capacity, the least recently used entry is evicted.
// If an entry already exists for this key, it is updated and moved to the front.
func (c *CredentialCache) Set(roleArn, identityToken string, creds *Credentials) {
	if creds == nil {
		return
	}

	key := buildCacheKey(roleArn, identityToken)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if entry already exists
	if entry, exists := c.cache[key]; exists {
		// Update existing entry
		entry.credentials = creds
		c.lru.MoveToFront(entry.element)
		return
	}

	// Evict LRU entry if at capacity
	if len(c.cache) >= c.maxSize {
		c.evictLRU()
	}

	// Create new entry
	entry := &cacheEntry{
		key:         key,
		credentials: creds,
	}
	entry.element = c.lru.PushFront(entry)
	c.cache[key] = entry
}

// Delete removes a cached entry for the given role ARN and identity token.
func (c *CredentialCache) Delete(roleArn, identityToken string) {
	key := buildCacheKey(roleArn, identityToken)

	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, exists := c.cache[key]; exists {
		c.lru.Remove(entry.element)
		delete(c.cache, key)
	}
}

// Clear removes all entries from the cache.
func (c *CredentialCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.cache = make(map[string]*cacheEntry)
	c.lru.Init()
}

// Size returns the current number of entries in the cache.
func (c *CredentialCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}

// evictLRU removes the least recently used entry from the cache.
// Must be called with write lock held.
func (c *CredentialCache) evictLRU() {
	if c.lru.Len() == 0 {
		return
	}

	// Get the oldest entry (back of list)
	oldest := c.lru.Back()
	if oldest == nil {
		return
	}

	entry := oldest.Value.(*cacheEntry)
	c.lru.Remove(oldest)
	delete(c.cache, entry.key)
}

// buildCacheKey creates a unique cache key from role ARN and identity token.
//
// The token is hashed with SHA-256 to:
//   - Prevent storing sensitive tokens in memory as keys
//   - Create fixed-length keys regardless of token size
//   - Naturally handle token rotation (new token = new key)
func buildCacheKey(roleArn, identityToken string) string {
	tokenHash := sha256.Sum256([]byte(identityToken))
	return fmt.Sprintf("%s:%s", roleArn, hex.EncodeToString(tokenHash[:]))
}
