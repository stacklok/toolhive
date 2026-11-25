// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"crypto/sha256"
	"fmt"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

const (
	// rejectedRefreshesMaxEntries bounds the memory of rejected refreshes. A
	// flood of rejections evicts old entries, which only costs one more IdP
	// call per evicted refresh token.
	rejectedRefreshesMaxEntries = 10_000

	// rejectedRefreshesTTL is how long a rejection is remembered. It is short
	// because some IdPs answer invalid_grant for account or policy state that
	// an administrator can fix without the user signing in again.
	rejectedRefreshesTTL = 10 * time.Minute
)

// rejectedRefreshKey identifies one refresh token in one storage row. The
// token is kept only as a SHA-256 digest.
type rejectedRefreshKey struct {
	row storage.UpstreamTokenRowID
	rt  [sha256.Size]byte
}

type rejectedRefresh struct {
	err       error
	expiresAt time.Time
}

// rejectedRefreshes remembers, per process, refresh tokens the IdP rejected
// with invalid_grant, so a row holding a dead refresh token does not cost an
// IdP call on every request. An entry is keyed on the row ID and the refresh
// token, so it stops matching as soon as another process or a new sign-in
// stores a different refresh token in the row.
//
// The nil and zero values are usable: lookups miss and adds do nothing.
//
// The error returned by lookup is shared by every caller that hits the entry;
// callers must treat it as read-only.
type rejectedRefreshes struct {
	cache *lru.Cache[rejectedRefreshKey, rejectedRefresh]
	ttl   time.Duration
	now   func() time.Time
}

func newRejectedRefreshes(size int, ttl time.Duration, now func() time.Time) (*rejectedRefreshes, error) {
	if size < 1 {
		return nil, fmt.Errorf("rejected refreshes size must be >= 1, got %d", size)
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("rejected refreshes ttl must be > 0, got %s", ttl)
	}
	if now == nil {
		now = time.Now
	}
	cache, err := lru.New[rejectedRefreshKey, rejectedRefresh](size)
	if err != nil {
		return nil, fmt.Errorf("failed to create rejected refreshes cache: %w", err)
	}
	return &rejectedRefreshes{cache: cache, ttl: ttl, now: now}, nil
}

// lookup returns the remembered rejection, or nil when there is none.
func (m *rejectedRefreshes) lookup(row storage.UpstreamTokenRowID, refreshToken string) error {
	if m == nil || m.cache == nil {
		return nil
	}
	key := newRejectedRefreshKey(row, refreshToken)
	entry, ok := m.cache.Get(key)
	if !ok {
		return nil
	}
	if !m.now().Before(entry.expiresAt) {
		m.cache.Remove(key)
		return nil
	}
	return entry.err
}
func (m *rejectedRefreshes) add(row storage.UpstreamTokenRowID, refreshToken string, err error) {
	if m == nil || m.cache == nil || err == nil {
		return
	}
	m.cache.Add(newRejectedRefreshKey(row, refreshToken), rejectedRefresh{
		err:       err,
		expiresAt: m.now().Add(m.ttl),
	})
}

func newRejectedRefreshKey(row storage.UpstreamTokenRowID, refreshToken string) rejectedRefreshKey {
	return rejectedRefreshKey{row: row, rt: sha256.Sum256([]byte(refreshToken))}
}
