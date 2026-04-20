// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// authErr wraps ErrAuthenticationFailed so errors.Is() matches.
func authErr(msg string) error {
	return fmt.Errorf("%w: %s", vmcp.ErrAuthenticationFailed, msg)
}

// stubBackendClient is a simple stub that returns a pre-configured sequence of errors/results.
type stubBackendClient struct {
	mu       sync.Mutex
	callErrs []error // errors to return in order (nil = success)
	callIdx  int
	calls    int
}

func (s *stubBackendClient) nextErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.callIdx >= len(s.callErrs) {
		return nil
	}
	err := s.callErrs[s.callIdx]
	s.callIdx++
	return err
}

func (s *stubBackendClient) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any, _ map[string]any) (*vmcp.ToolCallResult, error) {
	if err := s.nextErr(); err != nil {
		return nil, err
	}
	return &vmcp.ToolCallResult{}, nil
}

func (s *stubBackendClient) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) (*vmcp.ResourceReadResult, error) {
	if err := s.nextErr(); err != nil {
		return nil, err
	}
	return &vmcp.ResourceReadResult{}, nil
}

func (s *stubBackendClient) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	if err := s.nextErr(); err != nil {
		return nil, err
	}
	return &vmcp.PromptGetResult{}, nil
}

func (s *stubBackendClient) ListCapabilities(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	if err := s.nextErr(); err != nil {
		return nil, err
	}
	return &vmcp.CapabilityList{}, nil
}

func makeTarget(id string) *vmcp.BackendTarget {
	return &vmcp.BackendTarget{
		WorkloadID:    id,
		WorkloadName:  id,
		BaseURL:       "http://localhost:8080",
		TransportType: "streamable-http",
	}
}

// newFastRetryClient creates a retryingBackendClient with minimal backoff for tests.
func newFastRetryClient(inner vmcp.BackendClient) *retryingBackendClient {
	c := newRetryingBackendClient(inner, nil)
	c.initialBackoff = time.Millisecond // fast for tests
	c.tracer = noop.NewTracerProvider().Tracer("test")
	return c
}

// TestRetryingBackendClient_SuccessOnFirstAttempt verifies that operations that succeed
// immediately are passed through without any retry overhead.
func TestRetryingBackendClient_SuccessOnFirstAttempt(t *testing.T) {
	t.Parallel()

	stub := &stubBackendClient{callErrs: []error{nil}}
	c := newFastRetryClient(stub)
	target := makeTarget("backend-1")

	result, err := c.CallTool(context.Background(), target, "tool1", nil, nil)

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, 1, stub.calls)
}

// TestRetryingBackendClient_SuccessOnFirstAttempt_ResetsBreaker verifies that a first-attempt
// success resets the circuit breaker, so prior failures don't accumulate indefinitely.
func TestRetryingBackendClient_SuccessOnFirstAttempt_ResetsBreaker(t *testing.T) {
	t.Parallel()

	stub := &stubBackendClient{callErrs: []error{nil}}
	c := newFastRetryClient(stub)
	target := makeTarget("backend-reset-initial")

	// Prime the breaker with stale failures from a previous sequence.
	breaker := c.getBreaker(target.WorkloadID)
	breaker.consecutiveFails = 3

	// A first-attempt success must reset the breaker counter to zero.
	_, err := c.CallTool(context.Background(), target, "tool1", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, 0, breaker.consecutiveFails)
	assert.False(t, breaker.open)
}

// TestRetryingBackendClient_SuccessAfterAuthFailure verifies that a single 401 is retried
// successfully and the operation returns the successful result.
func TestRetryingBackendClient_SuccessAfterAuthFailure(t *testing.T) {
	t.Parallel()

	stub := &stubBackendClient{callErrs: []error{authErr("401 unauthorized"), nil}}
	c := newFastRetryClient(stub)
	target := makeTarget("backend-1")

	result, err := c.CallTool(context.Background(), target, "tool1", nil, nil)

	require.NoError(t, err)
	assert.NotNil(t, result)
	// First call + one retry
	assert.Equal(t, 2, stub.calls)
}

// TestRetryingBackendClient_MaxRetriesExhausted verifies that after maxAuthRetries, the
// last error is returned and no further retries are attempted.
func TestRetryingBackendClient_MaxRetriesExhausted(t *testing.T) {
	t.Parallel()

	// All calls fail with auth error (1 initial + maxAuthRetries retries)
	errs := make([]error, maxAuthRetries+1)
	for i := range errs {
		errs[i] = authErr("401 unauthorized")
	}
	stub := &stubBackendClient{callErrs: errs}
	c := newFastRetryClient(stub)
	target := makeTarget("backend-1")

	result, err := c.CallTool(context.Background(), target, "tool1", nil, nil)

	require.Error(t, err)
	assert.Nil(t, result)
	assert.True(t, errors.Is(err, vmcp.ErrAuthenticationFailed))
	// 1 initial attempt + maxAuthRetries retries
	assert.Equal(t, maxAuthRetries+1, stub.calls)
}

// TestRetryingBackendClient_NonAuthErrorNotRetried verifies that non-auth errors are
// returned immediately without any retry.
func TestRetryingBackendClient_NonAuthErrorNotRetried(t *testing.T) {
	t.Parallel()

	nonAuthErr := fmt.Errorf("%w: connection refused", vmcp.ErrBackendUnavailable)
	stub := &stubBackendClient{callErrs: []error{nonAuthErr}}
	c := newFastRetryClient(stub)
	target := makeTarget("backend-1")

	_, err := c.CallTool(context.Background(), target, "tool1", nil, nil)

	require.Error(t, err)
	assert.True(t, errors.Is(err, vmcp.ErrBackendUnavailable))
	// Only 1 call — no retries for non-auth errors
	assert.Equal(t, 1, stub.calls)
}

// TestRetryingBackendClient_CircuitBreakerOpens verifies that after N consecutive auth
// failures the circuit breaker opens and further retries are skipped.
func TestRetryingBackendClient_CircuitBreakerOpens(t *testing.T) {
	t.Parallel()

	stub := &stubBackendClient{}
	// Always return auth error
	for i := 0; i < 100; i++ {
		stub.callErrs = append(stub.callErrs, authErr("401 unauthorized"))
	}

	c := newFastRetryClient(stub)
	c.cbThreshold = 2 // open after 2 consecutive failures
	target := makeTarget("backend-cb")

	// Drive enough failures to open the circuit breaker.
	// Each CallTool call: 1 initial + maxAuthRetries retries = 4 calls total.
	// After cbThreshold (2) complete retry sequences fail, circuit opens.
	for i := 0; i < c.cbThreshold; i++ {
		_, err := c.CallTool(context.Background(), target, "tool1", nil, nil)
		require.Error(t, err)
	}

	// Circuit should now be open
	breaker := c.getBreaker(target.WorkloadID)
	assert.True(t, breaker.open, "circuit breaker should be open after threshold failures")

	// Further calls should not retry — only 1 attempt (the initial call)
	callsBefore := stub.calls
	_, err := c.CallTool(context.Background(), target, "tool1", nil, nil)
	require.Error(t, err)
	assert.Equal(t, callsBefore+1, stub.calls, "circuit open: should make exactly 1 call with no retries")
}

// TestRetryingBackendClient_CircuitBreakerResetOnSuccess verifies that the circuit breaker
// resets its counter after a successful operation.
func TestRetryingBackendClient_CircuitBreakerResetOnSuccess(t *testing.T) {
	t.Parallel()

	// Fail once, then succeed — should reset the failure counter
	stub := &stubBackendClient{callErrs: []error{authErr("401"), nil}}
	c := newFastRetryClient(stub)
	c.cbThreshold = 2
	target := makeTarget("backend-reset")

	_, err := c.CallTool(context.Background(), target, "tool1", nil, nil)
	require.NoError(t, err)

	breaker := c.getBreaker(target.WorkloadID)
	assert.Equal(t, 0, breaker.consecutiveFails)
	assert.False(t, breaker.open)
}

// TestRetryingBackendClient_ConcurrentFailuresDeduplicated verifies that concurrent auth
// failures for the same backend result in only one backoff wait per attempt (via singleflight).
func TestRetryingBackendClient_ConcurrentFailuresDeduplicated(t *testing.T) {
	t.Parallel()

	const concurrency = 10

	// failWG counts down each time the stub returns a failure. The backoffFn
	// (running inside singleflight) waits until all goroutines have completed
	// their initial failing call — at that point the other 9 are already
	// coalesced on sf.Do — then records the sleep and returns.
	var failWG sync.WaitGroup
	failWG.Add(concurrency)

	var opCount atomic.Int64
	inner := &countingBackendClient{
		callCount: &opCount,
		failFirst: true,
		failCount: concurrency, // first 'concurrency' calls return auth failure
		onFail:    failWG.Done, // called synchronously when a failure is returned
	}

	var sleepCount atomic.Int64
	c := newFastRetryClient(inner)
	// Inject a backoff hook that waits for all initial failures before proceeding.
	// Because backoffFn runs inside singleflight.Do, it is called exactly once;
	// all other goroutines block on sf.Do until this returns. Waiting for failWG
	// here guarantees they have all arrived and are coalesced before we assert.
	c.backoffFn = func(ctx context.Context, _ time.Duration) error {
		done := make(chan struct{})
		go func() { failWG.Wait(); close(done) }()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
		}
		sleepCount.Add(1)
		return nil
	}
	target := makeTarget("backend-concurrent")

	var wg sync.WaitGroup
	start := make(chan struct{})
	type callResult struct {
		result *vmcp.ToolCallResult
		err    error
	}
	results := make(chan callResult, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := c.CallTool(context.Background(), target, "tool1", nil, nil)
			results <- callResult{result, err}
		}()
	}

	close(start)
	wg.Wait()
	close(results)

	// All goroutines should succeed.
	for r := range results {
		require.NoError(t, r.err)
		assert.NotNil(t, r.result)
	}

	// singleflight must have coalesced: backoffFn fires exactly once for attempt 1,
	// not once per goroutine.
	assert.Equal(t, int64(1), sleepCount.Load(),
		"singleflight should coalesce backoff waits into a single sleep invocation")
}

// countingBackendClient is a thread-safe stub that returns auth errors for the first
// failCount calls when failFirst is set, then succeeds for all subsequent calls.
// onFail, if set, is called synchronously each time a failure is returned.
type countingBackendClient struct {
	callCount *atomic.Int64
	failFirst bool
	failCount int    // number of initial calls that return auth error (0 means just the first)
	onFail    func() // called each time a failure is returned
}

func (c *countingBackendClient) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any, _ map[string]any) (*vmcp.ToolCallResult, error) {
	n := c.callCount.Add(1)
	threshold := int64(1)
	if c.failCount > 0 {
		threshold = int64(c.failCount)
	}
	if c.failFirst && n <= threshold {
		if c.onFail != nil {
			c.onFail()
		}
		return nil, authErr("401 unauthorized")
	}
	return &vmcp.ToolCallResult{}, nil
}

func (*countingBackendClient) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) (*vmcp.ResourceReadResult, error) {
	return &vmcp.ResourceReadResult{}, nil
}

func (*countingBackendClient) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	return &vmcp.PromptGetResult{}, nil
}

func (*countingBackendClient) ListCapabilities(_ context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	return &vmcp.CapabilityList{}, nil
}

// TestRetryingBackendClient_AllMethods verifies that all four BackendClient methods go
// through the retry logic (success-after-failure scenario).
func TestRetryingBackendClient_AllMethods(t *testing.T) {
	t.Parallel()
	target := makeTarget("backend-all")

	t.Run("ReadResource retries on auth failure", func(t *testing.T) {
		t.Parallel()
		stub := &stubBackendClient{callErrs: []error{authErr("403 forbidden"), nil}}
		c := newFastRetryClient(stub)
		result, err := c.ReadResource(context.Background(), target, "res://foo")
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 2, stub.calls)
	})

	t.Run("GetPrompt retries on auth failure", func(t *testing.T) {
		t.Parallel()
		stub := &stubBackendClient{callErrs: []error{authErr("401 unauthorized"), nil}}
		c := newFastRetryClient(stub)
		result, err := c.GetPrompt(context.Background(), target, "my-prompt", nil)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 2, stub.calls)
	})

	t.Run("ListCapabilities retries on auth failure", func(t *testing.T) {
		t.Parallel()
		stub := &stubBackendClient{callErrs: []error{authErr("401 unauthorized"), nil}}
		c := newFastRetryClient(stub)
		result, err := c.ListCapabilities(context.Background(), target)
		require.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, 2, stub.calls)
	})
}

// TestRetryingBackendClient_ContextCancellation verifies that a cancelled context aborts
// the retry backoff cleanly.
func TestRetryingBackendClient_ContextCancellation(t *testing.T) {
	t.Parallel()

	stub := &stubBackendClient{}
	// Always auth-fail so the retry loop is entered
	for i := 0; i < 10; i++ {
		stub.callErrs = append(stub.callErrs, authErr("401"))
	}

	c := newFastRetryClient(stub)
	// Use a long backoff so context cancellation can interrupt it
	c.initialBackoff = 500 * time.Millisecond
	target := makeTarget("backend-ctx")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.CallTool(ctx, target, "tool1", nil, nil)
	require.Error(t, err)
	// Should get context deadline exceeded, not an auth error
	assert.True(t, errors.Is(err, context.DeadlineExceeded))
}
