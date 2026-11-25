// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"golang.org/x/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/authserver/upstream"
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

// skipRememberedRejectionsKey is the context key withoutRememberedRejections
// sets. RefreshAndStore detaches from the caller's cancellation and deadline
// with context.WithoutCancel and context.WithTimeout, both of which keep
// context values, so the marker reaches redeem.
//
// The marker is read inside the singleflight call for the row, so a caller
// that joins a call already in flight for the same row gets that call's
// result, which may have come from the memory. That window lasts as long as a
// memory lookup, and is accepted: giving the marked caller its own
// singleflight key would let two redemptions of the same refresh token run at
// once.
type skipRememberedRejectionsKey struct{}

// withoutRememberedRejections returns a context that makes redeem ask the IdP
// even when it remembers a rejection for the row. A fresh rejection is still
// remembered.
func withoutRememberedRejections(ctx context.Context) context.Context {
	return context.WithValue(ctx, skipRememberedRejectionsKey{}, true)
}

func skipsRememberedRejections(ctx context.Context) bool {
	skip, _ := ctx.Value(skipRememberedRejectionsKey{}).(bool)
	return skip
}

// rejectedRefreshKey identifies one rejected refresh in one storage row under
// one client registration. The tokens are kept only as a SHA-256 digest.
type rejectedRefreshKey struct {
	row storage.UpstreamTokenRowID
	// generation is the refresher's client registration generation when the
	// rejection was received; see upstreamTokenRefresher.bumpClientGeneration.
	generation uint64
	// digest covers the refresh token, the access token stored with it and
	// that access token's expiry. A non-rotating refresh elsewhere stores a
	// new access token, or the same access token string with a new expiry,
	// under the same refresh token, so the entry stops matching and the IdP is
	// asked again.
	digest [sha256.Size]byte
}

// rejectedRefresh is what is kept of a rejection: the OAuth error code and
// the HTTP status, never the provider's error text.
type rejectedRefresh struct {
	errorCode  string
	statusCode int
	status     string
	expiresAt  time.Time
}

// rejectedRefreshes remembers, per process, refresh tokens the IdP rejected
// with invalid_grant, so a row holding a dead refresh token does not cost an
// IdP call on every request. An entry is keyed on the row ID, the client
// registration generation, and a digest of the row's refresh token, access
// token and access token expiry, so it stops matching as soon as another
// process or a new sign-in stores different tokens or a new expiry in the row, or the upstream's client registration
// is replaced through the server's UpdateUpstreamClientCredentials. Replacing
// a registration directly on a provider bypasses the generation, and the
// memory keeps answering until the entry expires.
//
// A rejection is remembered only when its error chain holds an
// *oauth2.RetrieveError with a response; an OAuth2Provider that reports
// ErrGrantRejected without one is asked again on every request.
//
// The nil and zero values are usable: lookups miss and adds do nothing.
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

// lookup returns a new error describing the remembered rejection, or nil when
// there is none. Every call builds its own error.
func (m *rejectedRefreshes) lookup(
	generation uint64, rowID storage.UpstreamTokenRowID, tokens *storage.UpstreamTokens,
) error {
	if m == nil || m.cache == nil || tokens == nil {
		return nil
	}
	key := newRejectedRefreshKey(generation, rowID, tokens)
	entry, ok := m.cache.Get(key)
	if !ok {
		return nil
	}
	if !m.now().Before(entry.expiresAt) {
		m.cache.Remove(key)
		return nil
	}
	return fmt.Errorf(
		"upstream token refresh failed: %w (remembered from an earlier rejection; provider not called): %w",
		upstream.ErrGrantRejected,
		&oauth2.RetrieveError{
			Response:  &http.Response{StatusCode: entry.statusCode, Status: entry.status},
			ErrorCode: entry.errorCode,
		},
	)
}

// add remembers the rejection err for the row as stored in tokens until now+ttl. It
// does nothing on a nil or zero value, or when err holds no
// *oauth2.RetrieveError with a response to take the code and status from.
func (m *rejectedRefreshes) add(
	generation uint64, rowID storage.UpstreamTokenRowID, tokens *storage.UpstreamTokens, err error,
) {
	if m == nil || m.cache == nil || tokens == nil || err == nil {
		return
	}
	retrieveErr, ok := errors.AsType[*oauth2.RetrieveError](err)
	if !ok || retrieveErr.Response == nil {
		return
	}
	m.cache.Add(newRejectedRefreshKey(generation, rowID, tokens), rejectedRefresh{
		errorCode:  retrieveErr.ErrorCode,
		statusCode: retrieveErr.Response.StatusCode,
		status:     retrieveErr.Response.Status,
		expiresAt:  m.now().Add(m.ttl),
	})
}

func newRejectedRefreshKey(
	generation uint64, rowID storage.UpstreamTokenRowID, tokens *storage.UpstreamTokens,
) rejectedRefreshKey {
	h := sha256.New()
	// Length prefixes keep ("ab", "c") and ("a", "bc") apart.
	for _, v := range []string{tokens.RefreshToken, tokens.AccessToken} {
		_ = binary.Write(h, binary.BigEndian, uint64(len(v)))
		_, _ = h.Write([]byte(v))
	}
	_ = binary.Write(h, binary.BigEndian, tokens.ExpiresAt.UnixNano())
	key := rejectedRefreshKey{row: rowID, generation: generation}
	h.Sum(key.digest[:0])
	return key
}
