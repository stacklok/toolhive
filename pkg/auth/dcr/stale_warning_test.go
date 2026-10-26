// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package dcr

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// recordedLevels is a slog handler that keeps each record's level by message.
type recordedLevels struct {
	mu     sync.Mutex
	levels map[string][]slog.Level
}

func (*recordedLevels) Enabled(context.Context, slog.Level) bool { return true }
func (r *recordedLevels) WithAttrs([]slog.Attr) slog.Handler     { return r }
func (r *recordedLevels) WithGroup(string) slog.Handler          { return r }
func (r *recordedLevels) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.levels[rec.Message] = append(r.levels[rec.Message], rec.Level)
	return nil
}

// TestLookupCachedResolution_StaleWarningOnlyAtStart pins that a registration
// past the staleness threshold is reported at Warn on a start-time lookup and
// at Debug on a periodic re-check.
//
//nolint:paralleltest // slog.SetDefault is process-global
func TestLookupCachedResolution_StaleWarningOnlyAtStart(t *testing.T) {
	ctx := context.Background()
	store := newMemoryDCRStore(t)
	key := Key{
		Issuer:      "https://as.example.com",
		UpstreamID:  "https://idp.example.com/register",
		RedirectURI: "https://as.example.com/oauth/callback",
		ScopesHash:  storage.ScopesHash(nil),
	}
	_, err := store.PutIfAbsent(ctx, key, &Resolution{
		ClientID:              "old-client",
		AuthorizationEndpoint: "https://idp.example.com/authorize",
		TokenEndpoint:         "https://idp.example.com/token",
		CreatedAt:             time.Now().Add(-dcrStaleAgeThreshold - 24*time.Hour),
	})
	require.NoError(t, err)

	stale := func(periodic bool) []slog.Level {
		rec := &recordedLevels{levels: map[string][]slog.Level{}}
		previous := slog.Default()
		slog.SetDefault(slog.New(rec))
		t.Cleanup(func() { slog.SetDefault(previous) })
		_, hit, err := lookupCachedResolution(ctx, store, key, key.Issuer, key.RedirectURI, periodic)
		slog.SetDefault(previous)
		require.NoError(t, err)
		require.True(t, hit)
		for msg, levels := range rec.levels {
			if strings.Contains(msg, "exceeds staleness threshold") {
				return levels
			}
		}
		return nil
	}
	assert.Equal(t, []slog.Level{slog.LevelWarn}, stale(false))
	assert.Equal(t, []slog.Level{slog.LevelDebug}, stale(true))
}
