// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package untrusted

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrQuotaExceeded is returned when an admission control denies provisioning
// of an untrusted backend pod (per-user quota, create rate, per-server cap, or
// global capacity). Callers treat it as a soft failure: the backend is excluded
// from the session, matching partial-init semantics.
var ErrQuotaExceeded = errors.New("untrusted backend capacity exceeded")

// AdmissionConfig tunes the DoS admission controls enforced before any pod
// write. Zero-valued fields fall back to the documented defaults; the
// constructor rejects out-of-range values (fail loudly, per go-style).
type AdmissionConfig struct {
	// PerUserPodQuota caps concurrent untrusted pods per user across
	// MCPServers. Default 10.
	PerUserPodQuota int
	// PerUserCreateRate caps pod creations per user per minute. Default 5.
	PerUserCreateRate int
	// PerMCPServerCap caps concurrent untrusted pods per MCPServer UID.
	// Default 200.
	PerMCPServerCap int
	// GlobalCapFactor is the fraction of the session cache capacity that
	// bounds total untrusted pods (SCARD of the global pod set). Default 0.8.
	GlobalCapFactor float64
	// CacheCapacity is the session-manager cache capacity the global factor
	// applies to. Required when GlobalCapFactor is used.
	CacheCapacity int
}

const (
	defaultPerUserPodQuota   = 10
	defaultPerUserCreateRate = 5
	defaultPerMCPServerCap   = 200
	defaultGlobalCapFactor   = 0.8

	// rateLimitWindowTTL keeps per-minute rate keys for two windows.
	rateLimitWindowTTL = 2 * time.Minute
)

// resolved fills defaults and validates. Returns an error on out-of-range
// config (fail loudly rather than silently admitting).
func (c AdmissionConfig) resolved() (AdmissionConfig, error) {
	if c.PerUserPodQuota == 0 {
		c.PerUserPodQuota = defaultPerUserPodQuota
	}
	if c.PerUserCreateRate == 0 {
		c.PerUserCreateRate = defaultPerUserCreateRate
	}
	if c.PerMCPServerCap == 0 {
		c.PerMCPServerCap = defaultPerMCPServerCap
	}
	if c.GlobalCapFactor == 0 {
		c.GlobalCapFactor = defaultGlobalCapFactor
	}
	if c.PerUserPodQuota < 0 || c.PerUserCreateRate < 0 || c.PerMCPServerCap < 0 {
		return c, fmt.Errorf("untrusted admission: quota values must be non-negative")
	}
	if c.GlobalCapFactor <= 0 || c.GlobalCapFactor > 1 {
		return c, fmt.Errorf("untrusted admission: GlobalCapFactor must be in (0, 1], got %v", c.GlobalCapFactor)
	}
	if c.CacheCapacity <= 0 {
		return c, fmt.Errorf("untrusted admission: CacheCapacity must be positive, got %d", c.CacheCapacity)
	}
	return c, nil
}

// Admission is the DoS admission gate checked before any untrusted pod write.
type Admission interface {
	// Check admits or denies provisioning one pod for the (user, server) pair.
	// Returns nil to admit, an error wrapping ErrQuotaExceeded to deny.
	Check(ctx context.Context, userKey, mcpserverUID string) error
}

// admission enforces all DoS controls before any pod write. Every control
// fails closed when Redis is unavailable: an error reaching Redis denies
// admission, matching the existing Redis-required posture for multi-pod
// session metadata.
type admission struct {
	store *redisStore
	cfg   AdmissionConfig
	now   func() time.Time
}

// NewAdmission builds the admission gate from raw parts. Exported for tests
// and composition roots that wire components individually; production wiring
// goes through NewStack.
func NewAdmission(client redis.UniversalClient, keyPrefix, namespace string, cfg AdmissionConfig) (Admission, error) {
	store, err := newRedisStore(client, keyPrefix, namespace)
	if err != nil {
		return nil, err
	}
	return newAdmission(store, cfg, time.Now)
}

func newAdmission(store *redisStore, cfg AdmissionConfig, now func() time.Time) (*admission, error) {
	resolved, err := cfg.resolved()
	if err != nil {
		return nil, err
	}
	return &admission{store: store, cfg: resolved, now: now}, nil
}

// Check runs every admission control for the (user, server) pair. Returns nil
// to admit, ErrQuotaExceeded (wrapped with the control that fired) to deny.
// Checks run cheapest-first and before any pod write, so rejection costs only
// Redis reads.
//
// Every check here is a read-only pre-flight. The per-user quota counter is
// authoritatively enforced by the pod lifecycle's atomic INCR-with-cap inside
// recordPodCreate (an admitted race can never push the counter past the cap,
// and the over-quota pod is deleted by the caller); the reaper recomputes the
// counters from the authoritative pod LIST each tick.
func (a *admission) Check(ctx context.Context, userKey, mcpserverUID string) error {
	// Per-user concurrent quota (read-only pre-flight; see doc comment).
	quota, err := a.store.client.Get(ctx, a.store.userQuotaKey(userKey)).Int64()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("untrusted admission: quota check failed (failing closed): %w", err)
	}
	if quota >= int64(a.cfg.PerUserPodQuota) {
		return fmt.Errorf("%w: per-user pod quota (%d) reached", ErrQuotaExceeded, a.cfg.PerUserPodQuota)
	}

	// Creation rate limit: INCR + EXPIRE on the current minute bucket.
	rateKey := a.store.rateLimitKey(userKey, a.now())
	n, err := a.store.client.Incr(ctx, rateKey).Result()
	if err != nil {
		return fmt.Errorf("untrusted admission: rate-limit check failed (failing closed): %w", err)
	}
	if n == 1 {
		if err := a.store.client.Expire(ctx, rateKey, rateLimitWindowTTL).Err(); err != nil {
			return fmt.Errorf("untrusted admission: rate-limit expiry failed (failing closed): %w", err)
		}
	}
	if n > int64(a.cfg.PerUserCreateRate) {
		return fmt.Errorf("%w: per-user create rate (%d/min) exceeded", ErrQuotaExceeded, a.cfg.PerUserCreateRate)
	}

	// Per-MCPServer fan-out cap.
	serverCount, err := a.store.client.SCard(ctx, a.store.serverPodsSetKey(mcpserverUID)).Result()
	if err != nil {
		return fmt.Errorf("untrusted admission: per-server cap check failed (failing closed): %w", err)
	}
	if serverCount >= int64(a.cfg.PerMCPServerCap) {
		return fmt.Errorf("%w: per-MCPServer cap (%d) reached", ErrQuotaExceeded, a.cfg.PerMCPServerCap)
	}

	// vMCP-wide capacity.
	global, err := a.store.client.SCard(ctx, a.store.podsSetKey()).Result()
	if err != nil {
		return fmt.Errorf("untrusted admission: global capacity check failed (failing closed): %w", err)
	}
	globalCap := int64(float64(a.cfg.CacheCapacity) * a.cfg.GlobalCapFactor)
	if global >= globalCap {
		return fmt.Errorf("%w: global untrusted pod capacity (%d) reached", ErrQuotaExceeded, globalCap)
	}

	return nil
}
