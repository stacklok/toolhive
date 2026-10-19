// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// capacityWarnInterval is the minimum time between two cap-reached WARN lines.
const capacityWarnInterval = 15 * time.Minute

// capacityWarner logs registrations rejected at the DCR client cap at WARN, at
// most once per interval, so an operator without telemetry still learns why
// /oauth/register answers 503. The caller of /oauth/register is
// unauthenticated, so the volume is bounded by time rather than by request
// rate: rejections inside the interval are only counted, and the next line
// reports how many were held back. It deliberately logs neither the cap
// filling nor clearing, since a caller holding the cap flips between the two
// every time a slot expires.
type capacityWarner struct {
	interval time.Duration
	now      func() time.Time
	// counter reports the live count for the WARN line. Nil when the
	// storage backend cannot count DCR clients.
	counter storage.DCRClientCounter

	mu         sync.Mutex
	last       time.Time
	suppressed int64
}

// newCapacityWarner builds a capacityWarner for stor that logs at most once
// per capacityWarnInterval.
func newCapacityWarner(stor storage.Storage) *capacityWarner {
	counter, _ := storage.Unwrap(stor).(storage.DCRClientCounter)
	return &capacityWarner{interval: capacityWarnInterval, now: time.Now, counter: counter}
}

// rejected records one registration rejected with err, a ClientCapacityError,
// and logs it when the interval since the last line has passed. It is a no-op
// on a nil receiver, for Handlers built without NewHandler.
func (w *capacityWarner) rejected(ctx context.Context, err error) {
	if w == nil {
		return
	}
	w.mu.Lock()
	now := w.now()
	if !w.last.IsZero() && now.Sub(w.last) < w.interval {
		w.suppressed++
		w.mu.Unlock()
		return
	}
	w.last = now
	suppressed := w.suppressed
	w.suppressed = 0
	w.mu.Unlock()

	attrs := []any{
		"retry_after_seconds", capacityRetryAfterSeconds(err),
		"suppressed_rejections", suppressed,
	}
	var capErr *storage.ClientCapacityError
	if errors.As(err, &capErr) {
		attrs = append(attrs, "max_dcr_clients", capErr.Cap)
	}
	// One count per interval, so this read stays off the request path in
	// all but the request that logs.
	if w.counter != nil {
		if n, countErr := w.counter.CountDCRClients(ctx); countErr == nil {
			attrs = append(attrs, "dcr_clients", n)
		}
	}
	attrs = append(attrs, "setting", "storage.max_dcr_clients")
	slog.Warn("DCR client cap reached; rejecting new client registrations", attrs...)
}
