// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// fakeTokenCache is a minimal TokenCache whose Get returns preset values so the
// metered decorator's hit/miss classification can be exercised directly.
type fakeTokenCache struct {
	token *CachedToken
	err   error
}

func (f *fakeTokenCache) Get(context.Context, string) (*CachedToken, error) {
	return f.token, f.err
}
func (*fakeTokenCache) Set(context.Context, string, *CachedToken) error { return nil }
func (*fakeTokenCache) Delete(context.Context, string) error            { return nil }
func (*fakeTokenCache) Clear(context.Context) error                     { return nil }
func (*fakeTokenCache) Close() error                                    { return nil }

// resultCounts reads stacklok.vmcp.token_cache.requests and returns the counts
// keyed by the result label value.
func resultCounts(t *testing.T, reader sdkmetric.Reader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	counts := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "stacklok.vmcp.token_cache.requests" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "counter must be an int64 Sum")
			for _, dp := range sum.DataPoints {
				v, has := dp.Attributes.Value(attribute.Key("result"))
				require.True(t, has, "result label must be present")
				counts[v.AsString()] += dp.Value
			}
		}
	}
	return counts
}

func TestMeteredTokenCache_HitAndMiss(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		token      *CachedToken
		err        error
		wantResult string
	}{
		{
			name:       "valid token is a hit",
			token:      &CachedToken{Token: "t", ExpiresAt: time.Now().Add(time.Hour)},
			wantResult: "hit",
		},
		{
			name:       "nil token is a miss",
			token:      nil,
			wantResult: "miss",
		},
		{
			name:       "expired token is a miss",
			token:      &CachedToken{Token: "t", ExpiresAt: time.Now().Add(-time.Hour)},
			wantResult: "miss",
		},
		{
			name:       "error is a miss",
			token:      &CachedToken{Token: "t", ExpiresAt: time.Now().Add(time.Hour)},
			err:        errors.New("boom"),
			wantResult: "miss",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reader := sdkmetric.NewManualReader()
			mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			c := NewMeteredTokenCache(&fakeTokenCache{token: tt.token, err: tt.err}, mp)

			_, _ = c.Get(context.Background(), "key")

			counts := resultCounts(t, reader)
			assert.Equal(t, int64(1), counts[tt.wantResult])
			other := "miss"
			if tt.wantResult == "miss" {
				other = "hit"
			}
			assert.Zero(t, counts[other], "the other result must not be incremented")
		})
	}
}

func TestMeteredTokenCache_CountsAccumulate(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	hitToken := &CachedToken{Token: "t", ExpiresAt: time.Now().Add(time.Hour)}
	c := NewMeteredTokenCache(&fakeTokenCache{token: hitToken}, mp)
	_, _ = c.Get(context.Background(), "a")
	_, _ = c.Get(context.Background(), "b")

	miss := NewMeteredTokenCache(&fakeTokenCache{token: nil}, mp)
	_, _ = miss.Get(context.Background(), "c")

	counts := resultCounts(t, reader)
	assert.Equal(t, int64(2), counts["hit"])
	assert.Equal(t, int64(1), counts["miss"])
}

func TestNewMeteredTokenCache_NilProviderReturnsBase(t *testing.T) {
	t.Parallel()

	base := &fakeTokenCache{}
	assert.Same(t, base, NewMeteredTokenCache(base, nil).(*fakeTokenCache))
}
