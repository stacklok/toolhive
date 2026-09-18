// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// perIPLimiterEvictionThreshold is how many distinct IPs perIPLimiter tracks
// before it sweeps idle entries on the next call. Kept low deliberately:
// this limiter only ever guards a low-traffic, human-facing login page, so
// the map is never expected to hold more than a handful of concurrent
// callers, and a small threshold keeps the sweep itself cheap.
const perIPLimiterEvictionThreshold = 256

// perIPLimiterIdleTTL is how long an IP's bucket survives with no requests
// before eviction reclaims it.
const perIPLimiterIdleTTL = 10 * time.Minute

// perIPLimiter buckets golang.org/x/time/rate.Limiters by client IP, unlike
// registerLimiter/deviceAuthorizationLimiter/deviceVerificationLimiter's
// per-process bucket (see registerLimiter's doc comment in handler.go for
// why those endpoints are deliberately NOT per-IP). Device flow's
// verification page is different: it's a human-facing login page a
// legitimate user retries against (mistyped codes), so a single
// process-wide bucket lets one caller -- malicious or just persistent --
// burn the shared burst and starve every other concurrent login on the
// server. Per-IP scoping bounds that blast radius to one caller's own IP.
//
// Idle entries are swept inline (no background goroutine to manage the
// lifecycle of) once the map grows past perIPLimiterEvictionThreshold, so
// long-lived processes don't grow this map unboundedly.
type perIPLimiter struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiterEntry
	limit    rate.Limit
	burst    int
}

type ipLimiterEntry struct {
	limiter    *rate.Limiter
	lastSeenAt time.Time
}

// newPerIPLimiter returns a perIPLimiter granting each distinct IP its own
// token bucket at the given rate/burst.
func newPerIPLimiter(limit rate.Limit, burst int) *perIPLimiter {
	return &perIPLimiter{
		limiters: make(map[string]*ipLimiterEntry),
		limit:    limit,
		burst:    burst,
	}
}

// allow reports whether a request from ip may proceed, consuming one token
// from that IP's bucket if so.
func (l *perIPLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	entry, ok := l.limiters[ip]
	if !ok {
		entry = &ipLimiterEntry{limiter: rate.NewLimiter(l.limit, l.burst)}
		l.limiters[ip] = entry
	}
	entry.lastSeenAt = now

	if len(l.limiters) > perIPLimiterEvictionThreshold {
		l.evictIdleLocked(now)
	}
	return entry.limiter.Allow()
}

// evictIdleLocked removes buckets untouched for longer than
// perIPLimiterIdleTTL. Callers must hold l.mu.
func (l *perIPLimiter) evictIdleLocked(now time.Time) {
	for ip, entry := range l.limiters {
		if now.Sub(entry.lastSeenAt) > perIPLimiterIdleTTL {
			delete(l.limiters, ip)
		}
	}
}

// clientIP extracts the request's client IP from RemoteAddr, deliberately
// ignoring X-Forwarded-For and similar headers: those are attacker-controlled
// input without a trusted, correctly-configured reverse proxy in front of
// this server (see registerLimiter's doc comment in handler.go for the same
// reasoning), and trusting them here would let a client forge a distinct IP
// per request and defeat the very throttling this limiter exists to provide.
func clientIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}
	return host
}
