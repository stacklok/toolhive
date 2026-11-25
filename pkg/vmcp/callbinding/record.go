// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"context"
	"sync"
	"time"
)

// Call is the call a call-bound token started.
type Call struct {
	// SessionID is the Mcp-Session-Id the call was sent on. It is empty for a
	// sessionless (Modern) request.
	SessionID string
	// RequestID is the JSON-RPC ID of the tools/call, as its JSON encoding:
	// 7 and "7" are different IDs.
	RequestID string
	// Started is when the vMCP admitted the call.
	Started time.Time
}

// Record is the single-use record of call-bound tokens. A key names one token
// (the vMCP uses its issuer and jti). Each key can start one call; the record
// remembers the key until the expiry given to Start, whether or not the call
// has ended, so the token cannot start a second call while it could still be
// accepted.
//
// Implementations must be safe for concurrent use. NewMemoryRecord keeps the
// record in this process only, so a replay to another replica is not seen.
type Record interface {
	// Start records key as used by call until expiresAt, and reports true. It
	// reports false, and changes nothing, when key is already recorded.
	// Recording must be atomic: of two concurrent Starts for one key, exactly
	// one reports true.
	Start(ctx context.Context, key string, call Call, expiresAt time.Time) (bool, error)
	// End marks the call recorded under key as ended. The key stays used until
	// its expiry. Ending an unknown or already ended key is not an error.
	End(ctx context.Context, key string) error
	// OpenCall returns the call recorded under key while it has not ended or
	// expired.
	OpenCall(ctx context.Context, key string) (Call, bool, error)
}

// MemoryRecord is a Record held in this process's memory.
type MemoryRecord struct {
	now func() time.Time

	mu        sync.Mutex
	entries   map[string]*memoryEntry
	nextSweep time.Time
}

type memoryEntry struct {
	call      Call
	ended     bool
	expiresAt time.Time
}

// memorySweepInterval is how often Start drops expired entries. An expired
// entry is never returned in between, so the interval bounds only memory.
const memorySweepInterval = time.Minute

var _ Record = (*MemoryRecord)(nil)

// NewMemoryRecord returns an empty in-memory Record. It starts no goroutine:
// expired entries are dropped during Start.
func NewMemoryRecord() *MemoryRecord {
	return &MemoryRecord{now: time.Now, entries: make(map[string]*memoryEntry)}
}

// Start implements Record.
func (m *MemoryRecord) Start(_ context.Context, key string, call Call, expiresAt time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.sweepLocked(now)
	if e, ok := m.entries[key]; ok && now.Before(e.expiresAt) {
		return false, nil
	}
	m.entries[key] = &memoryEntry{call: call, expiresAt: expiresAt}
	return true, nil
}

// End implements Record.
func (m *MemoryRecord) End(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[key]; ok {
		e.ended = true
	}
	return nil
}

// OpenCall implements Record.
func (m *MemoryRecord) OpenCall(_ context.Context, key string) (Call, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok || e.ended || !m.now().Before(e.expiresAt) {
		return Call{}, false, nil
	}
	return e.call, true, nil
}

// sweepLocked drops expired entries at most once per memorySweepInterval.
func (m *MemoryRecord) sweepLocked(now time.Time) {
	if now.Before(m.nextSweep) {
		return
	}
	for key, e := range m.entries {
		if !now.Before(e.expiresAt) {
			delete(m.entries, key)
		}
	}
	m.nextSweep = now.Add(memorySweepInterval)
}
