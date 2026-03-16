// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/singleflight"

	"github.com/stacklok/toolhive/pkg/vmcp"
	vmcpauth "github.com/stacklok/toolhive/pkg/vmcp/auth"
)

const (
	// authRetryInstrumentationName is the OpenTelemetry instrumentation scope for auth retries.
	authRetryInstrumentationName = "github.com/stacklok/toolhive/pkg/vmcp/client"

	// maxAuthRetries is the maximum number of retry attempts after an auth failure.
	maxAuthRetries = 3

	// authCircuitBreakerThreshold is the number of consecutive auth failures before
	// the circuit breaker opens and disables further retries for a backend.
	authCircuitBreakerThreshold = 5

	// initialRetryBackoff is the base duration for exponential backoff between retries.
	// Attempt 1: 100ms, Attempt 2: 200ms, Attempt 3: 400ms.
	initialRetryBackoff = 100 * time.Millisecond
)

// authCircuitBreaker tracks consecutive auth failures per backend and opens the circuit
// after too many failures to prevent excessive latency from repeated auth retries.
type authCircuitBreaker struct {
	mu               sync.Mutex
	consecutiveFails int
	open             bool
}

// canRetry returns true if auth retries are still allowed (circuit is closed).
func (cb *authCircuitBreaker) canRetry() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return !cb.open
}

// recordSuccess resets the consecutive failure counter and closes the circuit.
func (cb *authCircuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails = 0
	cb.open = false
}

// recordFailure increments the failure counter and opens the circuit if the threshold is exceeded.
func (cb *authCircuitBreaker) recordFailure(threshold int, backendID string) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.consecutiveFails++
	if !cb.open && cb.consecutiveFails >= threshold {
		cb.open = true
		slog.Warn("auth circuit breaker opened: too many consecutive auth failures, disabling retries",
			"backend", backendID, "consecutive_failures", cb.consecutiveFails)
	}
}

// retryingBackendClient wraps a BackendClient and automatically retries operations that
// fail due to authentication errors (401/403). It uses:
//   - Exponential backoff with a maximum of [maxAuthRetries] attempts
//   - A per-backend circuit breaker to stop retrying after [authCircuitBreakerThreshold] consecutive failures
//   - singleflight to deduplicate concurrent backoff waits for the same backend
//   - OpenTelemetry spans to surface auth-retry latency in distributed traces
//
// Raw credentials are never logged.
type retryingBackendClient struct {
	inner    vmcp.BackendClient
	registry vmcpauth.OutgoingAuthRegistry

	// sf deduplicates concurrent backoff waits for the same backend at the same attempt number.
	sf singleflight.Group

	// breakers maps backendID -> *authCircuitBreaker. LoadOrStore is used for concurrent safety.
	breakers sync.Map

	tracer         trace.Tracer
	maxRetries     int
	cbThreshold    int
	initialBackoff time.Duration

	// backoffFn is the sleep function used inside singleflight. nil uses time.After.
	// Tests inject a counted hook to assert coalescing without real wall-clock delays.
	backoffFn func(ctx context.Context, d time.Duration) error
}

// newRetryingBackendClient wraps inner with auth-failure retry logic.
func newRetryingBackendClient(inner vmcp.BackendClient, registry vmcpauth.OutgoingAuthRegistry) *retryingBackendClient {
	return &retryingBackendClient{
		inner:          inner,
		registry:       registry,
		tracer:         otel.Tracer(authRetryInstrumentationName),
		maxRetries:     maxAuthRetries,
		cbThreshold:    authCircuitBreakerThreshold,
		initialBackoff: initialRetryBackoff,
	}
}

// getBreaker returns (or lazily creates) the auth circuit breaker for a backend.
func (r *retryingBackendClient) getBreaker(backendID string) *authCircuitBreaker {
	v, _ := r.breakers.LoadOrStore(backendID, &authCircuitBreaker{})
	return v.(*authCircuitBreaker) //nolint:forcetypeassert
}

// withAuthRetry executes op, and if it returns ErrAuthenticationFailed, retries up to
// r.maxRetries times with exponential backoff, using singleflight to deduplicate concurrent
// backoff waits per backend. Auth-retry overhead is surfaced as an OpenTelemetry span.
func (r *retryingBackendClient) withAuthRetry(
	ctx context.Context,
	backendID string,
	op func(context.Context) error,
) error {
	breaker := r.getBreaker(backendID)

	err := op(ctx)
	if err == nil {
		breaker.recordSuccess()
		return nil
	}
	if !errors.Is(err, vmcp.ErrAuthenticationFailed) {
		return err
	}
	if !breaker.canRetry() {
		slog.Debug("auth circuit breaker open, skipping auth retry",
			"backend", backendID)
		return err
	}

	// Start a span to surface auth-retry latency in distributed traces.
	ctx, span := r.tracer.Start(ctx, "auth.retry",
		trace.WithAttributes(
			attribute.String("target.workload_id", backendID),
			attribute.Int("max_retries", r.maxRetries),
		),
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	lastErr := err
	backoff := r.initialBackoff
	for attempt := 1; attempt <= r.maxRetries; attempt++ {
		// Use singleflight to deduplicate concurrent backoff waits for the same backend
		// and attempt number. The first goroutine sleeps; the others coalesce with it.
		// DoChan is used instead of Do so every caller can also select on its own
		// ctx.Done() — otherwise a coalesced caller with a short deadline would be
		// stuck for the full backoff duration of the leader's longer-lived context.
		sfKey := fmt.Sprintf("%s:attempt:%d", backendID, attempt)
		// The singleflight function uses a detached context so that a cancelled
		// leader goroutine does not propagate its error to all coalesced callers.
		// Per-caller cancellation is handled by the outer select on ctx.Done() below.
		detachedCtx := context.WithoutCancel(ctx)
		currentBackoff := backoff
		ch := r.sf.DoChan(sfKey, func() (any, error) {
			if r.backoffFn != nil {
				return nil, r.backoffFn(detachedCtx, currentBackoff)
			}
			select {
			case <-detachedCtx.Done():
				return nil, detachedCtx.Err()
			case <-time.After(currentBackoff):
				return nil, nil
			}
		})
		var sfErr error
		select {
		case <-ctx.Done():
			sfErr = ctx.Err()
		case res := <-ch:
			sfErr = res.Err
		}
		if sfErr != nil {
			span.RecordError(sfErr)
			return sfErr
		}

		span.AddEvent("auth.retry.attempt",
			trace.WithAttributes(attribute.Int("attempt", attempt)))

		retryErr := op(ctx)
		if retryErr == nil {
			breaker.recordSuccess()
			span.SetStatus(codes.Ok, "auth retry succeeded")
			return nil
		}

		lastErr = retryErr
		if !errors.Is(retryErr, vmcp.ErrAuthenticationFailed) {
			// Non-auth error on retry — no point continuing auth retries.
			span.RecordError(retryErr)
			return retryErr
		}
		backoff *= 2
	}

	// All retries exhausted with auth failures — update circuit breaker.
	breaker.recordFailure(r.cbThreshold, backendID)
	span.RecordError(lastErr)
	span.SetStatus(codes.Error, "auth retry exhausted")
	return lastErr
}

// retryResult is a generic helper that wraps withAuthRetry for operations that return a value,
// eliminating the boilerplate of capturing a result variable in every BackendClient method.
func retryResult[T any](
	ctx context.Context, r *retryingBackendClient, backendID string, op func(context.Context) (T, error),
) (T, error) {
	var result T
	err := r.withAuthRetry(ctx, backendID, func(ctx context.Context) error {
		var opErr error
		result, opErr = op(ctx)
		return opErr
	})
	return result, err
}

// CallTool implements vmcp.BackendClient.
func (r *retryingBackendClient) CallTool(
	ctx context.Context,
	target *vmcp.BackendTarget,
	toolName string,
	arguments map[string]any,
	meta map[string]any,
) (*vmcp.ToolCallResult, error) {
	return retryResult(ctx, r, target.WorkloadID, func(ctx context.Context) (*vmcp.ToolCallResult, error) {
		return r.inner.CallTool(ctx, target, toolName, arguments, meta)
	})
}

// ReadResource implements vmcp.BackendClient.
func (r *retryingBackendClient) ReadResource(
	ctx context.Context,
	target *vmcp.BackendTarget,
	uri string,
) (*vmcp.ResourceReadResult, error) {
	return retryResult(ctx, r, target.WorkloadID, func(ctx context.Context) (*vmcp.ResourceReadResult, error) {
		return r.inner.ReadResource(ctx, target, uri)
	})
}

// GetPrompt implements vmcp.BackendClient.
func (r *retryingBackendClient) GetPrompt(
	ctx context.Context,
	target *vmcp.BackendTarget,
	name string,
	arguments map[string]any,
) (*vmcp.PromptGetResult, error) {
	return retryResult(ctx, r, target.WorkloadID, func(ctx context.Context) (*vmcp.PromptGetResult, error) {
		return r.inner.GetPrompt(ctx, target, name, arguments)
	})
}

// ListCapabilities implements vmcp.BackendClient.
func (r *retryingBackendClient) ListCapabilities(
	ctx context.Context,
	target *vmcp.BackendTarget,
) (*vmcp.CapabilityList, error) {
	return retryResult(ctx, r, target.WorkloadID, func(ctx context.Context) (*vmcp.CapabilityList, error) {
		return r.inner.ListCapabilities(ctx, target)
	})
}
