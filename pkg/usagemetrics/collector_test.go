// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usagemetrics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// report is one request received by a reportServer.
type report struct {
	header http.Header
	body   map[string]any
}

// reportServer is a local stand-in for the usage metrics endpoint.
type reportServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	reports []report
}

func newReportServer(t *testing.T) *reportServer {
	t.Helper()
	rs := &reportServer{}
	rs.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		var body map[string]any
		assert.NoError(t, json.Unmarshal(data, &body))
		rs.mu.Lock()
		rs.reports = append(rs.reports, report{header: r.Header.Clone(), body: body})
		rs.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rs.server.Close)
	return rs
}

// attach points the collector's client at the server, with an anonymous ID so
// reports are sent. It must be called before the collector flushes.
func (rs *reportServer) attach(c *Collector) {
	c.client.endpoint = rs.server.URL
	c.client.client = rs.server.Client()
	c.client.anonymousID = "test-anon-id"
}

func (rs *reportServer) received() []report {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]report(nil), rs.reports...)
}

// newTestCollector returns a collector that reports to a local server and is
// shut down when the test ends, so no test reaches the real endpoint.
func newTestCollector(t *testing.T, opts ...CollectorOption) (*Collector, *reportServer) {
	t.Helper()
	collector, err := NewCollector(opts...)
	require.NoError(t, err)
	rs := newReportServer(t)
	rs.attach(collector)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		collector.Shutdown(ctx)
	})
	return collector, rs
}

func TestNewCollector(t *testing.T) {
	t.Parallel()

	collector, _ := newTestCollector(t)

	// Verify initial state
	assert.NotEmpty(t, collector.instanceID, "Instance ID should be generated")
	assert.Equal(t, int64(0), collector.GetCurrentCount(), "Initial count should be 0")
	assert.NotEmpty(t, collector.currentDate, "Current date should be set")
}

func TestCollector_IncrementToolCall(t *testing.T) {
	t.Parallel()

	collector, _ := newTestCollector(t)

	// Verify initial count
	assert.Equal(t, int64(0), collector.GetCurrentCount())

	// Increment once
	collector.IncrementToolCall()
	assert.Equal(t, int64(1), collector.GetCurrentCount())

	// Increment multiple times
	for i := 0; i < 10; i++ {
		collector.IncrementToolCall()
	}
	assert.Equal(t, int64(11), collector.GetCurrentCount())
}

func TestCollector_Shutdown(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)

	// Increment some calls
	collector.IncrementToolCall()
	collector.IncrementToolCall()

	ctx := context.Background()

	// Shutdown should not error
	collector.Shutdown(ctx)

	// Second shutdown should be idempotent
	collector.Shutdown(ctx)

	reports := rs.received()
	require.Len(t, reports, 1, "shutdown sends one final report")
	assert.Equal(t, float64(2), reports[0].body["count"])
}

func TestCollector_ShutdownWithoutCallsSendsNoReport(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)
	collector.Start()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	collector.Shutdown(ctx)

	assert.Empty(t, rs.received(), "shutdown with no tool calls sends nothing")
}

func TestCollector_Start_PreventsDuplicateGoroutines(t *testing.T) {
	t.Parallel()

	collector, _ := newTestCollector(t)

	// Call Start multiple times
	collector.Start()
	collector.Start()
	collector.Start()

	// Verify started flag is set
	assert.True(t, collector.started.Load(), "Collector should be marked as started")

	// If multiple goroutines were created, we'd see issues with concurrent
	// access to the channels. The test passes if no race conditions occur.
	// The -race flag in our test suite will catch this.
}

func TestShouldEnableMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		configDisabled bool
		envVarValue    string
		isCI           bool
		expected       bool
	}{
		{
			name:           "default enabled",
			configDisabled: false,
			envVarValue:    "",
			isCI:           false,
			expected:       true,
		},
		{
			name:           "config disabled",
			configDisabled: true,
			envVarValue:    "",
			isCI:           false,
			expected:       false,
		},
		{
			name:           "env var opt-out",
			configDisabled: false,
			envVarValue:    "false",
			isCI:           false,
			expected:       false,
		},
		{
			name:           "config disabled overrides env enabled",
			configDisabled: true,
			envVarValue:    "true",
			isCI:           false,
			expected:       false,
		},
		{
			name:           "CI environment disables metrics",
			configDisabled: false,
			envVarValue:    "",
			isCI:           true,
			expected:       false,
		},
		{
			name:           "CI environment overrides config and env",
			configDisabled: false,
			envVarValue:    "true",
			isCI:           true,
			expected:       false,
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Mock CI detection
			mockIsCI := func() bool {
				return tt.isCI
			}

			// Mock environment variable getter
			mockGetEnv := func(key string) string {
				if key == EnvVarUsageMetricsEnabled {
					return tt.envVarValue
				}
				return ""
			}

			result := shouldEnableMetrics(tt.configDisabled, mockIsCI, mockGetEnv)
			assert.Equal(t, tt.expected, result, "shouldEnableMetrics(%v) = %v, want %v", tt.configDisabled, result, tt.expected)
		})
	}
}

func TestCollector_FlushPayload(t *testing.T) {
	t.Parallel()

	var profileCalls atomic.Int32
	backends := 0
	collector, rs := newTestCollector(t, WithProfile(func() Profile {
		profileCalls.Add(1)
		return Profile{
			Transport:      "sse",
			ServerKind:     ServerKindContainer,
			RegistrySource: ValueBuiltin,
			CatalogServer:  "fetch",
			Runtime:        "secret-runtime",
			AuthMode:       AuthModeOIDC,
			Authz:          ValueOff,
			BackendCount:   &backends,
			Features:       []string{FeatureOptimizer, "not-a-feature"},
		}
	}))

	collector.IncrementToolCallFrom("claude-code")
	collector.IncrementToolCallFrom("Claude Code")
	collector.IncrementToolCall()
	require.NoError(t, collector.Flush())
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 2)
	assert.Equal(t, int32(2), profileCalls.Load(), "profile is read on every flush")

	body := reports[0].body
	assert.Equal(t, map[string]any{
		"count":           float64(3),
		"timestamp":       body["timestamp"],
		"calls_by_client": map[string]any{"claude-code": float64(2), ClientUnknown: float64(1)},
		"transport":       "sse",
		"server_kind":     ServerKindContainer,
		"registry_source": ValueBuiltin,
		"catalog_server":  "fetch",
		"runtime":         ValueOther,
		"auth_mode":       AuthModeOIDC,
		"authz":           ValueOff,
		"backend_count":   float64(0),
		"features":        []any{FeatureOptimizer},
	}, body)

	// A normal flush does not reset the counts.
	assert.Equal(t, float64(3), reports[1].body["count"])
}

func TestCollector_FlushWithoutProfileOmitsProfileFields(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 1)
	assert.Equal(t, float64(0), reports[0].body["count"])
	assert.NotContains(t, reports[0].body, "calls_by_client")
	assert.NotContains(t, reports[0].body, "transport")
}

func TestCollector_FlushAtMidnightResetsCounts(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	collector.currentDate = yesterday

	collector.IncrementToolCallFrom("cursor")
	collector.IncrementToolCallFrom("cursor")
	collector.IncrementToolCallFrom("zed")
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 1)
	assert.Equal(t, float64(3), reports[0].body["count"])
	assert.Equal(t, yesterday+"T23:59:00Z", reports[0].body["timestamp"])
	assert.Equal(t, map[string]any{"cursor": float64(2), "zed": float64(1)}, reports[0].body["calls_by_client"])

	snap := collector.snapshotCounts()
	assert.Zero(t, snap.total)
	assert.Empty(t, snap.byClient)
	assert.Equal(t, getCurrentDateUTC(), collector.currentDate)
}

func TestCollector_ReportsCodeModeCounts(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)
	collector.IncrementToolCallFrom("cursor")
	collector.IncrementToolCallFrom("cursor")
	collector.IncrementCodeModeCall()
	collector.AddCodeModeToolCalls(5)
	collector.AddCodeModeToolCalls(0)  // ignored
	collector.AddCodeModeToolCalls(-3) // ignored
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 1)
	body := reports[0].body
	assert.Equal(t, float64(2), body["count"], "count is unchanged by code mode tallies")
	assert.Equal(t, float64(1), body["code_mode_calls"])
	assert.Equal(t, float64(5), body["code_mode_tool_calls"])
}

func TestCollector_OmitsZeroCodeModeCounts(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)
	collector.IncrementToolCallFrom("cursor")
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 1)
	assert.NotContains(t, reports[0].body, "code_mode_calls")
	assert.NotContains(t, reports[0].body, "code_mode_tool_calls")
}

func TestCollector_FlushAtMidnightResetsCodeModeCounts(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t)
	collector.currentDate = time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	collector.IncrementToolCallFrom("cursor")
	collector.IncrementCodeModeCall()
	collector.AddCodeModeToolCalls(4)
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 1)
	assert.Equal(t, float64(1), reports[0].body["code_mode_calls"])
	assert.Equal(t, float64(4), reports[0].body["code_mode_tool_calls"])

	snap := collector.snapshotCounts()
	assert.Zero(t, snap.codeModeCalls)
	assert.Zero(t, snap.codeModeToolCalls)
}

func TestCollector_WithComponentSetsUserAgent(t *testing.T) {
	t.Parallel()

	collector, rs := newTestCollector(t, WithComponent("vmcp"))
	require.NoError(t, collector.Flush())

	reports := rs.received()
	require.Len(t, reports, 1)
	userAgent := reports[0].header.Get(userAgentHeader)
	assert.True(t, strings.HasPrefix(userAgent, "toolhive/vmcp "), "User-Agent: %s", userAgent)
	assert.Equal(t, "test-anon-id", reports[0].header.Get(anonymousIDHeader))
}
