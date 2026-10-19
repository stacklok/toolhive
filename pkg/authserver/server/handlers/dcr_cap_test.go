// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"golang.org/x/time/rate"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/storage"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestCapacityRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "whole seconds", err: &storage.ClientCapacityError{RetryAfter: 90 * time.Second}, want: 90},
		{name: "rounds up", err: &storage.ClientCapacityError{RetryAfter: 90*time.Second + time.Millisecond}, want: 91},
		{name: "sub-second is one", err: &storage.ClientCapacityError{RetryAfter: 500 * time.Millisecond}, want: 1},
		{name: "zero is one", err: &storage.ClientCapacityError{}, want: 1},
		{name: "wrapped", err: fmt.Errorf("wrap: %w", &storage.ClientCapacityError{RetryAfter: time.Hour}), want: 3600},
		{name: "bare sentinel is one", err: storage.ErrClientCapacity, want: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, capacityRetryAfterSeconds(tc.err))
		})
	}
}

// newRegisterTestHandler builds a Handler through NewHandler, so the register
// limiter and DCR metrics are wired as in production.
func newRegisterTestHandler(t *testing.T, stor storage.Storage, opts ...Option) *Handler {
	t.Helper()
	cfg := &server.AuthorizationServerConfig{
		Config:          &fosite.Config{AccessTokenIssuer: "https://test-authserver"},
		ScopesSupported: registration.DefaultScopes,
	}
	// An upstream keeps the handler out of token-only mode, which accepts
	// only private_key_jwt registrations.
	upstreams := []NamedUpstream{{Name: "test-upstream", Provider: &mockIDPProvider{}}}
	h, err := NewHandler(nil, cfg, stor, upstreams, opts...)
	require.NoError(t, err)
	return h
}

func postRegister(t *testing.T, routes http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(oauthproto.DynamicClientRegistrationRequest{
		RedirectURIs: []string{"http://127.0.0.1:8080/callback"},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/oauth/register", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, req)
	return w
}

func TestNewHandler_RegisterRateLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []Option
		wantRate  rate.Limit
		wantBurst int
		wantErr   bool
	}{
		{name: "default", wantRate: 1, wantBurst: 5},
		{name: "configured", opts: []Option{WithRegisterRateLimit(0.5, 2)}, wantRate: 0.5, wantBurst: 2},
		{name: "zero rate rejected", opts: []Option{WithRegisterRateLimit(0, 5)}, wantErr: true},
		{name: "zero burst rejected", opts: []Option{WithRegisterRateLimit(1, 0)}, wantErr: true},
		{name: "NaN rate rejected", opts: []Option{WithRegisterRateLimit(rate.Limit(math.NaN()), 5)}, wantErr: true},
		{name: "Inf rate rejected", opts: []Option{WithRegisterRateLimit(rate.Inf, 5)}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stor := storage.NewMemoryStorage()
			t.Cleanup(func() { _ = stor.Close() })
			cfg := &server.AuthorizationServerConfig{Config: &fosite.Config{AccessTokenIssuer: "https://test-authserver"}}

			h, err := NewHandler(nil, cfg, stor, nil, tc.opts...)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRate, h.registerLimiter.Limit())
			assert.Equal(t, tc.wantBurst, h.registerLimiter.Burst())
		})
	}
}

// TestRegister_ConfiguredBurstThen429 checks that /oauth/register follows a
// configured burst: exactly that many requests pass before the 429.
func TestRegister_ConfiguredBurstThen429(t *testing.T) {
	t.Parallel()

	stor := storage.NewMemoryStorage()
	t.Cleanup(func() { _ = stor.Close() })
	routes := newRegisterTestHandler(t, stor, WithRegisterRateLimit(0.001, 3)).Routes()

	for i := range 3 {
		assert.Equal(t, http.StatusCreated, postRegister(t, routes).Code, "burst request %d", i)
	}
	w := postRegister(t, routes)
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Equal(t, "1000", w.Header().Get("Retry-After"),
		"at 0.001/s the next token is 1000s away")
}

func TestLimiterRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rate rate.Limit
		want int
	}{
		{rate: 1, want: 1},
		{rate: 10, want: 1},
		{rate: 0.5, want: 2},
		{rate: 0.3, want: 4},
		{rate: 1e-20, want: math.MaxInt32},
	}
	for _, tc := range tests {
		t.Run(fmt.Sprint(tc.rate), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, limiterRetryAfterSeconds(rate.NewLimiter(tc.rate, 1)))
		})
	}
}

// TestRegister_CapRejectionAndMetrics drives /oauth/register through the cap
// and the rate limit, checking the 503 response and both DCR metrics. The
// store is wrapped in the CIMD decorator, as in production, so the gauge must
// reach the counter through storage.Unwrap.
func TestRegister_CapRejectionAndMetrics(t *testing.T) {
	t.Parallel()

	base := storage.NewMemoryStorage(storage.WithMaxDCRClients(1))
	t.Cleanup(func() { _ = base.Close() })
	stor, err := storage.NewCIMDStorageDecorator(base, storage.CIMDDecoratorConfig{
		Enabled: true, CacheMaxSize: 1, FallbackTTL: time.Minute,
	})
	require.NoError(t, err)
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	// Burst 2 with a negligible refill: the first request fills the cap, the
	// second is rejected at the cap, the third is rate limited.
	h := newRegisterTestHandler(t, stor, WithMeterProvider(mp), WithRegisterRateLimit(0.001, 2))
	routes := h.Routes()

	require.Equal(t, http.StatusCreated, postRegister(t, routes).Code)
	assert.Equal(t, int64(0), rejectionCount(t, reader), "a successful registration is not a rejection")
	assert.Equal(t, int64(1), liveClientGauge(t, reader))

	w := postRegister(t, routes)
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var errResp registration.DCRError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &errResp))
	assert.Equal(t, "server_error", errResp.Error)
	// The time until the only client expires, less whatever wall-clock time
	// passed since it registered.
	retryAfter, err := strconv.Atoi(w.Header().Get("Retry-After"))
	require.NoError(t, err)
	ttl := int(storage.DefaultUnusedDCRClientTTL.Seconds())
	assert.InDelta(t, ttl-1, retryAfter, 1, "Retry-After is the time until the only client expires")
	assert.Equal(t, int64(1), rejectionCount(t, reader))

	require.Equal(t, http.StatusTooManyRequests, postRegister(t, routes).Code)
	assert.Equal(t, int64(1), rejectionCount(t, reader), "a 429 is not a capacity rejection")
	assert.Equal(t, int64(1), liveClientGauge(t, reader))

	require.NoError(t, h.Close())
	assert.Nil(t, collectDCRMetric(t, reader, "toolhive_authserver_dcr_clients"),
		"Close must unregister the gauge callback")
}

func collectDCRMetric(t *testing.T, reader *sdkmetric.ManualReader, name string) *metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for i := range sm.Metrics {
			if sm.Metrics[i].Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

func rejectionCount(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	m := collectDCRMetric(t, reader, "toolhive_authserver_dcr_capacity_rejections")
	if m == nil {
		return 0
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok)
	var total int64
	for _, dp := range sum.DataPoints {
		total += dp.Value
	}
	return total
}

func liveClientGauge(t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()
	m := collectDCRMetric(t, reader, "toolhive_authserver_dcr_clients")
	require.NotNil(t, m)
	gauge, ok := m.Data.(metricdata.Gauge[int64])
	require.True(t, ok)
	require.Len(t, gauge.DataPoints, 1)
	return gauge.DataPoints[0].Value
}
