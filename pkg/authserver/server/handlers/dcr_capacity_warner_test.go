// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// captureWarnLines swaps in a JSON slog handler at warn level for the duration
// of the test and returns a function decoding every line logged so far.
// Process-global, not safe for t.Parallel().
func captureWarnLines(t *testing.T) func() []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(orig) })
	return func() []map[string]any {
		var lines []map[string]any
		scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
		for scanner.Scan() {
			var line map[string]any
			require.NoError(t, json.Unmarshal(scanner.Bytes(), &line))
			lines = append(lines, line)
		}
		return lines
	}
}

// TestCapacityWarner_RateLimitsAndReportsSuppressed checks that cap rejections
// log one WARN per interval carrying the cap, the live count, Retry-After and
// how many rejections were held back since the previous line.
//
//nolint:paralleltest // captures the process-global slog default
func TestCapacityWarner_RateLimitsAndReportsSuppressed(t *testing.T) {
	lines := captureWarnLines(t)
	ctx := context.Background()

	stor := storage.NewMemoryStorage(storage.WithMaxDCRClients(2))
	t.Cleanup(func() { _ = stor.Close() })
	for _, id := range []string{"dcr-1", "dcr-2"} {
		client, err := registration.New(registration.Config{
			ID:                      id,
			TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone,
			RedirectURIs:            []string{"http://127.0.0.1/callback"},
		})
		require.NoError(t, err)
		require.NoError(t, stor.RegisterClient(ctx, client))
	}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var elapsed time.Duration
	w := newCapacityWarner(stor)
	w.now = func() time.Time { return start.Add(elapsed) }
	capErr := &storage.ClientCapacityError{Cap: 2, RetryAfter: 90 * time.Second}

	for _, elapsed = range []time.Duration{0, time.Minute, capacityWarnInterval - time.Second} {
		w.rejected(ctx, capErr)
	}
	require.Len(t, lines(), 1, "rejections inside the interval must not log")

	elapsed = capacityWarnInterval
	w.rejected(ctx, capErr)

	got := lines()
	require.Len(t, got, 2)
	for i, wantSuppressed := range []float64{0, 2} {
		assert.Equal(t, "DCR client cap reached; rejecting new client registrations", got[i]["msg"])
		assert.Equal(t, wantSuppressed, got[i]["suppressed_rejections"], "line %d", i)
		assert.Equal(t, float64(2), got[i]["max_dcr_clients"])
		assert.Equal(t, float64(2), got[i]["dcr_clients"])
		assert.Equal(t, float64(90), got[i]["retry_after_seconds"])
		assert.Equal(t, "storage.max_dcr_clients", got[i]["setting"])
	}
}

// TestCapacityWarner_WithoutCounter checks the WARN omits the live count when
// the backend cannot count DCR clients, and that a nil warner is a no-op.
//
//nolint:paralleltest // captures the process-global slog default
func TestCapacityWarner_WithoutCounter(t *testing.T) {
	lines := captureWarnLines(t)

	var nilWarner *capacityWarner
	nilWarner.rejected(context.Background(), storage.ErrClientCapacity)
	require.Empty(t, lines())

	w := &capacityWarner{interval: capacityWarnInterval, now: time.Now}
	w.rejected(context.Background(), storage.ErrClientCapacity)

	got := lines()
	require.Len(t, got, 1)
	assert.NotContains(t, got[0], "dcr_clients")
	assert.NotContains(t, got[0], "max_dcr_clients", "a bare sentinel carries no cap")
	assert.Equal(t, float64(1), got[0]["retry_after_seconds"])
}
