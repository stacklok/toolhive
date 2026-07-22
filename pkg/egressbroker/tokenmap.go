// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"container/list"
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenMap is the request-id → injected-token correlation the response
// scanner (ADR D6c) needs: ext_authz records (x-request-id → scan record) at
// injection time; ext_proc looks it up on the response.
//
// Retention discipline: no token plaintext is RETAINED at rest in this map.
// The entry carries the SHA-256 of the injected header value (the correlation
// proof) plus the derived scan needles (exact + base64 of the header value),
// computed at injection time — the only moment the plaintext is legitimately
// in scope. The needles live in process memory only, are never logged, never
// leave the process, and are dropped on lookup (one injection ↔ one scanned
// response) or TTL eviction.
//
// Bounds: entries expire after a TTL (the request/response round trip plus
// slack) and the whole map is capped; under load the oldest entries are
// evicted (a response whose entry was evicted scans as unknown-request and
// passes under the fail-open default — recorded as a scan miss).
//
// Concurrency: a single mutex guards map + eviction list (one synchronization
// primitive per data structure).
type TokenMap struct {
	mu         sync.Mutex
	entries    map[string]*list.Element // requestID → list element
	lru        *list.List               // front = most recently recorded
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

// ScanRecord is one entry's payload.
type ScanRecord struct {
	TokenHash [32]byte
	Needles   [][]byte
	Provider  string
}

type tokenMapEntry struct {
	requestID string
	record    ScanRecord
	expiresAt time.Time
}

// NewTokenMap builds the bounded TTL map. ttl must be positive; maxEntries must be
// >= 1 (fail loudly on nonsensical bounds — a zero bound would silently
// disable the scanner's correlation and every response would pass as
// unknown-request).
func NewTokenMap(ttl time.Duration, maxEntries int) (*TokenMap, error) {
	return NewTokenMapWithClock(ttl, maxEntries, time.Now)
}

// NewTokenMapWithClock is NewTokenMap with an injectable clock (tests use a
// fake clock instead of time.Sleep against the TTL).
func NewTokenMapWithClock(ttl time.Duration, maxEntries int, now func() time.Time) (*TokenMap, error) {
	if ttl <= 0 {
		return nil, fmt.Errorf("egressbroker: token map TTL must be positive")
	}
	if maxEntries < 1 {
		return nil, fmt.Errorf("egressbroker: token map bound must be >= 1")
	}
	if now == nil {
		return nil, fmt.Errorf("egressbroker: clock must not be nil")
	}
	return &TokenMap{
		entries:    map[string]*list.Element{},
		lru:        list.New(),
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        now,
	}, nil
}

// Record stores the scan record for an injected credential header value under
// requestID. provider travels with the entry for scan-time metrics/audit
// labeling (low-cardinality policy names only). A re-recorded requestID
// refreshes in place.
func (m *TokenMap) Record(requestID, headerValue, provider string) {
	if requestID == "" || headerValue == "" {
		return
	}
	entry := &tokenMapEntry{
		requestID: requestID,
		record: ScanRecord{
			TokenHash: hashOf(headerValue),
			Needles:   buildNeedles(headerValue),
			Provider:  provider,
		},
		expiresAt: m.now().Add(m.ttl),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.entries[requestID]; ok {
		el.Value = entry
		m.lru.MoveToFront(el)
		return
	}
	m.entries[requestID] = m.lru.PushFront(entry)
	for m.lru.Len() > m.maxEntries {
		oldest := m.lru.Back()
		m.lru.Remove(oldest)
		delete(m.entries, oldest.Value.(*tokenMapEntry).requestID)
	}
}

// Lookup returns the scan record for requestID, consuming the entry (one
// injection corresponds to one scanned response; a second lookup reports
// unknown). Expired entries count as unknown.
func (m *TokenMap) Lookup(requestID string) (ScanRecord, bool) {
	if requestID == "" {
		return ScanRecord{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	el, found := m.entries[requestID]
	if !found {
		return ScanRecord{}, false
	}
	delete(m.entries, requestID)
	m.lru.Remove(el)
	entry := el.Value.(*tokenMapEntry)
	if m.now().After(entry.expiresAt) {
		return ScanRecord{}, false
	}
	return entry.record, true
}

// evictExpired drops all expired entries (background hygiene; lookup
// already treats expired entries as unknown, so this only bounds memory
// under a flood of distinct request-ids within one TTL window).
func (m *TokenMap) evictExpired() {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for el := m.lru.Back(); el != nil; {
		prev := el.Prev()
		entry := el.Value.(*tokenMapEntry)
		if now.After(entry.expiresAt) {
			m.lru.Remove(el)
			delete(m.entries, entry.requestID)
		}
		el = prev
	}
}

// Len returns the live entry count.
func (m *TokenMap) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lru.Len()
}

// RunEvictionLoop sweeps expired entries every interval until ctx is done
// (goroutine owned by the caller; exits cleanly on cancellation — no leak).
func (m *TokenMap) RunEvictionLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.evictExpired()
		}
	}
}
