// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
)

// defaultSkew is how far before a token's reported expiry the cache stops
// serving it, so a token is never handed out moments before it expires.
const defaultSkew = 60 * time.Second

// errExchangePanicked is handed to every caller coalesced on an exchange whose
// delegate panicked. The exchange runs on a detached goroutine, so the panic is
// recovered there — an unrecovered panic in a background goroutine would crash
// the process — and surfaced as this error rather than a zero token; the
// in-flight key is freed either way.
var errExchangePanicked = errors.New("obo/cache: underlying token exchange panicked")

// Cache decorates an exchange.TokenExchanger with a session-partitioned, in-memory
// token cache and implements exchange.Exchanger. It is safe for concurrent use.
//
// Concurrent misses for the same (userToken, audience) are coalesced by
// single-flight: only one underlying exchange runs at a time per key, and the
// other callers wait for and share its result. This keeps a burst of requests
// for one session — the common case when a cached token reaches its expiry —
// from stampeding the token endpoint. The shared exchange runs on its own
// goroutine under a context detached from any single caller
// (context.WithoutCancel), so one caller's cancellation cannot fail the others;
// each caller is still released early by its own context. See fetch / runExchange.
type Cache struct {
	delegate exchange.TokenExchanger
	skew     time.Duration
	now      func() time.Time

	mu       sync.Mutex
	entries  map[string]entry
	inflight map[string]*call
}

// entry is a cached exchanged token and the instant it expires.
type entry struct {
	token  string
	expiry time.Time
}

// call is a single in-flight exchange that concurrent callers for the same key
// share. done is closed once token/err are populated.
type call struct {
	done  chan struct{}
	token exchange.Token
	err   error
}

// Compile-time assertion that Cache satisfies the caller-facing contract.
var _ exchange.Exchanger = (*Cache)(nil)

// Option configures a Cache at construction time.
type Option func(*Cache)

// WithSkew overrides the expiry skew (default 60s). A token is treated as stale
// once the current time is within skew of its expiry. A negative skew panics.
func WithSkew(skew time.Duration) Option {
	return func(c *Cache) { c.skew = skew }
}

// WithClock overrides the time source, primarily for deterministic tests. A nil
// argument is ignored and the default (time.Now) is kept.
func WithClock(now func() time.Time) Option {
	return func(c *Cache) {
		if now != nil {
			c.now = now
		}
	}
}

// New returns a Cache fronting delegate. It panics if delegate is nil or if an
// option sets a negative skew — both are programming errors that should surface
// at wiring time, not on the first exchange.
func New(delegate exchange.TokenExchanger, opts ...Option) *Cache {
	if delegate == nil {
		panic("obo/cache: delegate TokenExchanger is nil")
	}
	c := &Cache{
		delegate: delegate,
		skew:     defaultSkew,
		now:      time.Now,
		entries:  make(map[string]entry),
		inflight: make(map[string]*call),
	}
	for _, opt := range opts {
		opt(c)
	}
	if c.skew < 0 {
		panic("obo/cache: skew must not be negative")
	}
	return c
}

// Exchange implements exchange.Exchanger. It returns a cached token for
// (userToken, audience) when one is present and not within the skew of its
// expiry; otherwise it delegates to the underlying TokenExchanger, caches the
// result (when it carries an expiry), and returns it. Concurrent misses for the
// same key are coalesced into a single underlying exchange.
func (c *Cache) Exchange(ctx context.Context, userToken, audience string) (string, error) {
	key := cacheKey(userToken, audience)

	if token, ok := c.lookup(key); ok {
		return token, nil
	}

	token, err := c.fetch(ctx, key, userToken, audience)
	if err != nil {
		return "", err
	}
	return token.AccessToken, nil
}

// lookup returns the cached token for key when present and still fresh. This is
// the lock-protected fast path; it does not consult in-flight exchanges.
func (c *Cache) lookup(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.entries[key]
	if !ok || !c.freshLocked(e) {
		return "", false
	}
	return e.token, true
}

// fetch resolves a cache miss. It serves a token another flight just cached,
// joins an in-flight exchange for the same key, or starts one. The exchange runs
// on a detached goroutine (see runExchange) so it survives any single caller's
// cancellation; every caller — including the one that started the flight — then
// waits via waitFor and is released early by its own context.
func (c *Cache) fetch(ctx context.Context, key, userToken, audience string) (exchange.Token, error) {
	c.mu.Lock()
	// Re-check under the lock: another flight may have cached the token between
	// the lock-free lookup and now.
	if e, ok := c.entries[key]; ok && c.freshLocked(e) {
		c.mu.Unlock()
		return exchange.Token{AccessToken: e.token, Expiry: e.expiry}, nil
	}
	flight, ok := c.inflight[key]
	if !ok {
		flight = &call{done: make(chan struct{})}
		c.inflight[key] = flight
		// Detach from this caller's context so a later cancellation of whichever
		// caller happened to start the flight does not abort an exchange the
		// coalesced followers still need. The exchange is bounded by the
		// delegate's own timeout (e.g. the HTTP client Timeout).
		go c.runExchange(context.WithoutCancel(ctx), key, userToken, audience, flight)
	}
	c.mu.Unlock()

	return waitFor(ctx, flight)
}

// runExchange performs the single shared exchange for key on its own goroutine
// and, in a defer, frees the in-flight slot, caches a successful expiring token,
// and releases the waiters. ctx is already detached from any caller (see fetch),
// so the exchange is not aborted by a caller's cancellation. Because it runs
// detached, a panicking delegate would otherwise crash the process: the defer
// recovers it, hands every waiter errExchangePanicked instead of a zero token,
// and still frees the key. A token with no reported expiry cannot be aged out
// safely, so it is used once and never stored (see exchange.Token).
func (c *Cache) runExchange(ctx context.Context, key, userToken, audience string, flight *call) {
	defer func() {
		if r := recover(); r != nil {
			// Surface the delegate bug instead of black-holing it: log the panic
			// value and stack at WARN. It is deliberately not embedded in the
			// caller-facing error, since a panic payload could rarely carry
			// sensitive data.
			slog.Warn("obo/cache: token exchange delegate panicked; recovered",
				"recovered", r, "stack", string(debug.Stack()))
			flight.err = errExchangePanicked
		}
		c.mu.Lock()
		delete(c.inflight, key)
		if flight.err == nil && !flight.token.Expiry.IsZero() {
			c.sweepLocked()
			c.entries[key] = entry{token: flight.token.AccessToken, expiry: flight.token.Expiry}
		}
		c.mu.Unlock()
		close(flight.done)
	}()

	flight.token, flight.err = c.delegate.ExchangeToken(ctx, userToken, audience)
}

// waitFor blocks until the in-flight exchange completes and returns its shared
// result, or returns early if the caller's own context is cancelled first (so a
// follower is never held past its own deadline by a slow leader).
func waitFor(ctx context.Context, c *call) (exchange.Token, error) {
	select {
	case <-ctx.Done():
		return exchange.Token{}, ctx.Err()
	case <-c.done:
		return c.token, c.err
	}
}

// freshLocked reports whether e is still serveable: the current time is before
// its expiry minus the skew. The caller must hold c.mu.
func (c *Cache) freshLocked(e entry) bool {
	return c.now().Before(e.expiry.Add(-c.skew))
}

// sweepLocked deletes entries whose expiry has already passed, bounding the map
// to tokens that are still live and keeping memory proportional to the number of
// live sessions without a background goroutine. The caller must hold c.mu.
//
// It is O(n) in the number of cached entries and runs on every cacheable write,
// so a burst of N first-time exchanges across distinct sessions is O(n²) overall,
// serialized on c.mu. This is acceptable for v1 — the work is in-memory under the
// lock, not a network stall (the delegate call happens outside the lock) — but if
// profiling shows lock contention, switch to a throttled or amortized sweep
// (e.g. at most once per K writes or T seconds).
func (c *Cache) sweepLocked() {
	now := c.now()
	for k, e := range c.entries {
		if !now.Before(e.expiry) {
			delete(c.entries, k)
		}
	}
}

// cacheKey partitions the cache by session: it hashes the (secret) user token
// so the raw token is not retained as a map key, and appends the (non-secret)
// audience. The hash is fixed-width hex, so the ":" delimiter is unambiguous and
// distinct (userToken, audience) pairs cannot collide.
func cacheKey(userToken, audience string) string {
	sum := sha256.Sum256([]byte(userToken))
	return hex.EncodeToString(sum[:]) + ":" + audience
}
