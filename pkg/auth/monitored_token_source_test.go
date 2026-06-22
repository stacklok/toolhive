// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/oauthproto/oauthtest"
	statusMocks "github.com/stacklok/toolhive/pkg/workloads/statuses/mocks"
)

// mockStatusUpdater adapts a mock statuses.StatusManager to auth.StatusUpdater for testing
type mockStatusUpdater struct {
	sm *statusMocks.MockStatusManager
}

func newMockStatusUpdater(ctrl *gomock.Controller) (*mockStatusUpdater, *statusMocks.MockStatusManager) {
	mockSM := statusMocks.NewMockStatusManager(ctrl)
	return &mockStatusUpdater{sm: mockSM}, mockSM
}

func (m *mockStatusUpdater) SetWorkloadStatus(ctx context.Context, workloadName string, status rt.WorkloadStatus, reason string) error {
	return m.sm.SetWorkloadStatus(ctx, workloadName, status, reason)
}

// mockTokenSource is a simple mock implementation of oauth2.TokenSource for testing.
// It uses a callback function to allow flexible token/error configuration.
type mockTokenSource struct {
	mu        sync.Mutex
	tokenFn   func() (*oauth2.Token, error)
	callCount int
	notifyAt  int
	notify    chan struct{}
}

func newMockTokenSource() *mockTokenSource {
	return &mockTokenSource{
		tokenFn: func() (*oauth2.Token, error) {
			return nil, errors.New("no token configured")
		},
	}
}

func (m *mockTokenSource) setTokenFn(fn func() (*oauth2.Token, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenFn = fn
}

// notifyOnCall returns a channel that is closed when Token() is called for the nth time.
// Useful in tests to synchronise without time.Sleep.
func (m *mockTokenSource) notifyOnCall(n int) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan struct{})
	m.notifyAt = n
	m.notify = ch
	return ch
}

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	tok, err := m.tokenFn()
	if m.notify != nil && m.callCount >= m.notifyAt {
		close(m.notify)
		m.notify = nil
	}
	return tok, err
}

// createRetrieveError creates an error for testing token failures. ErrorCode
// is left unset, mirroring what golang.org/x/oauth2 produces when the response
// body is not a parseable RFC 6749 error response (e.g. an HTML page from a
// WAF or load balancer).
func createRetrieveError(statusCode int, body string) *oauth2.RetrieveError {
	response := &http.Response{
		StatusCode: statusCode,
		Body:       http.NoBody,
	}
	return &oauth2.RetrieveError{
		Response: response,
		Body:     []byte(body),
	}
}

// createRetrieveErrorWithCode is like createRetrieveError but also sets the
// ErrorCode field, mirroring what golang.org/x/oauth2 populates when the
// server responds with a parseable JSON error body containing an "error"
// field.
func createRetrieveErrorWithCode(statusCode int, errorCode, body string) *oauth2.RetrieveError {
	err := createRetrieveError(statusCode, body)
	err.ErrorCode = errorCode
	return err
}

func TestMonitoredTokenSource_SuccessfulTokenRetrieval(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, _ := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	validToken := &oauth2.Token{
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		Expiry:       time.Now().Add(time.Hour),
	}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return validToken, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	// Test successful token retrieval
	token, err := ats.Token()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if token.AccessToken != "test-access-token" {
		t.Errorf("Expected access token 'test-access-token', got %s", token.AccessToken)
	}

	// Should not have called SetWorkloadStatus for successful retrieval
	// (no expectations set means we expect it not to be called)
}

func TestMonitoredTokenSource_AuthenticationErrorMarksUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// Create an error that simulates token retrieval failure
	retrieveErr := createRetrieveErrorWithCode(http.StatusBadRequest, "invalid_grant", `{"error":"invalid_grant","error_description":"refresh token expired"}`)
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	// Expect SetWorkloadStatus to be called with unauthenticated status
	statusManager.EXPECT().
		SetWorkloadStatus(
			gomock.Any(),
			"test-workload",
			rt.WorkloadStatusUnauthenticated,
			gomock.Any(),
		).
		DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, reason string) error {
			if !strings.Contains(reason, "invalid_grant") {
				t.Errorf("Expected reason to contain 'invalid_grant', got %s", reason)
			}
			return nil
		}).
		Times(1)

	// Token retrieval should fail and mark as unauthenticated
	_, err := ats.Token()
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	// Give a moment for the async call to complete
	time.Sleep(50 * time.Millisecond)
}

func TestMonitoredTokenSource_ErrorMarksUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// Any error should mark as unauthenticated
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, errors.New("some generic error")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	// Expect SetWorkloadStatus to be called for any error
	statusManager.EXPECT().
		SetWorkloadStatus(
			gomock.Any(),
			"test-workload",
			rt.WorkloadStatusUnauthenticated,
			gomock.Any(),
		).
		Return(nil).
		Times(1)

	// Token retrieval should fail and mark as unauthenticated
	_, err := ats.Token()
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	// Give a moment for the async call to complete
	time.Sleep(50 * time.Millisecond)
}

func TestMonitoredTokenSource_BackgroundMonitoring(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	callCount := 0
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		callCount++
		if callCount == 1 {
			// First call: return valid token with short expiry
			return &oauth2.Token{
				AccessToken:  "test-token",
				RefreshToken: "test-refresh",
				Expiry:       time.Now().Add(500 * time.Millisecond),
			}, nil
		}
		// Subsequent calls: return authentication error
		retrieveErr := createRetrieveErrorWithCode(http.StatusUnauthorized, "invalid_token", `{"error":"invalid_token"}`)
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	// Expect SetWorkloadStatus to be called when auth error occurs
	statusManager.EXPECT().
		SetWorkloadStatus(
			gomock.Any(),
			"test-workload",
			rt.WorkloadStatusUnauthenticated,
			gomock.Any(),
		).
		Return(nil).
		Times(1)

	ats.StartBackgroundMonitoring()

	// Wait for token to expire and background monitoring to detect failure
	// The timer is scheduled for when token expires (500ms), then it processes the error
	// Need enough time for: initial timer (1ms) + token expiry (500ms) + error processing
	time.Sleep(2 * time.Second)

	// Verify monitoring stopped by checking that SetWorkloadStatus was called
	// (the mock expectations already verify this)
}

func TestMonitoredTokenSource_BackgroundMonitoringStopsOnAnyError(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	callCount := 0
	// Use a generic error - should mark as unauthenticated and stop monitoring
	genericErr := errors.New("network timeout")
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		callCount++
		if callCount == 1 {
			// First call: return valid token with short expiry
			return &oauth2.Token{
				AccessToken:  "test-token",
				RefreshToken: "test-refresh",
				Expiry:       time.Now().Add(500 * time.Millisecond),
			}, nil
		}
		// Subsequent calls: return generic error (should mark as unauthenticated)
		return nil, genericErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	// Expect SetWorkloadStatus to be called when any error occurs
	statusManager.EXPECT().
		SetWorkloadStatus(
			gomock.Any(),
			"test-workload",
			rt.WorkloadStatusUnauthenticated,
			gomock.Any(),
		).
		Return(nil).
		Times(1)

	ats.StartBackgroundMonitoring()

	// Wait for token to expire and background monitoring to detect failure
	// Flow: initial timer (1ms) → first check (gets token) → reschedule → wait → second check (gets error) → mark unauthenticated
	time.Sleep(2 * time.Second)

	// Verify monitoring stopped by checking that SetWorkloadStatus was called
	// (the mock expectations already verify this)
}

func TestMonitoredTokenSource_ExpiredTokenHandling(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, _ := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// Return an already-expired token (oauth2 library should try to refresh)
	expiredToken := &oauth2.Token{
		AccessToken:  "expired-token",
		RefreshToken: "refresh-token",
		Expiry:       time.Now().Add(-time.Hour),
	}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return expiredToken, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	// Should not mark as unauthenticated just for expired token
	// (oauth2 library should handle refresh; we only mark on actual auth errors)
	// (no expectations set means we expect SetWorkloadStatus not to be called)

	ats.StartBackgroundMonitoring()

	// Wait a bit for monitoring to check
	time.Sleep(200 * time.Millisecond)

	cancel()
}

func TestMonitoredTokenSource_StopMonitoring(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, _ := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return &oauth2.Token{
			AccessToken:  "test-token",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)
	ats.StartBackgroundMonitoring()

	// Wait a bit to ensure monitoring started
	time.Sleep(100 * time.Millisecond)

	// Stop monitoring via context cancellation
	cancel()

	// Wait a bit for monitoring to stop
	time.Sleep(100 * time.Millisecond)

	// Verify monitoring stopped - context cancellation is handled internally
	// We can verify by ensuring no unexpected SetWorkloadStatus calls
	// (test passes if no errors occur)
}

func TestMonitoredTokenSource_MultipleCallsToToken(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	retrieveErr := createRetrieveErrorWithCode(http.StatusUnauthorized, "invalid_token", `{"error":"invalid_token"}`)
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)

	statusManager.EXPECT().
		SetWorkloadStatus(
			gomock.Any(),
			"test-workload",
			rt.WorkloadStatusUnauthenticated,
			gomock.Any(),
		).
		Return(nil).
		Times(3) // Each Token() call will mark it

	// Call Token() multiple times
	for i := 0; i < 3; i++ {
		_, err := ats.Token()
		if err == nil {
			t.Fatal("Expected error, got nil")
		}
	}

	time.Sleep(50 * time.Millisecond)
}

// TestTransientRefresher_SingleflightDeduplicatesConcurrentRetries verifies the
// one property the refresher owns: it calls singleflight.Group.Do with a
// constant key, so concurrent Refresh() invocations are coalesced. Singleflight
// itself is a stable library; we don't re-validate its dedup semantics here.
//
// A regression that swapped the constant "token-refresh" key for something
// per-caller (e.g. a per-upstream key) would let the follower start its own
// flight, doubling the Token() call count, and this test would catch it.
func TestTransientRefresher_SingleflightDeduplicatesConcurrentRetries(t *testing.T) {
	t.Parallel()

	tokenSource := newMockTokenSource()
	recoveredToken := &oauth2.Token{
		AccessToken: "recovered-token",
		Expiry:      time.Now().Add(time.Hour),
	}

	// Block the leader inside the singleflight callback so the follower has
	// a window to join the in-flight entry.
	releaseLeader := make(chan struct{})
	leaderEntered := make(chan struct{})
	var leaderOnce sync.Once
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		leaderOnce.Do(func() { close(leaderEntered) })
		<-releaseLeader
		return recoveredToken, nil
	})

	transientErr := &net.OpError{
		Op: "dial", Net: "tcp",
		Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED},
	}

	ctx := context.Background()
	refresher := newTransientRefresher(tokenSource, "test-workload", "", "", fastBackOff)

	type result struct {
		tok *oauth2.Token
		err error
	}

	leaderResult := make(chan result, 1)
	go func() {
		tok, err := refresher.Refresh(ctx, transientErr)
		leaderResult <- result{tok, err}
	}()

	// Wait until the leader is provably inside Token() — only then is it safe
	// to spawn a follower that will deterministically join this flight.
	select {
	case <-leaderEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("leader did not enter Token() within 5s")
	}

	followerResult := make(chan result, 1)
	go func() {
		tok, err := refresher.Refresh(ctx, transientErr)
		followerResult <- result{tok, err}
	}()

	// Short sleep to let the follower call group.Do and join the in-flight
	// entry before we release the leader. This is the same technique used by
	// golang.org/x/sync's own singleflight tests — singleflight exposes no
	// observable "follower is queued" signal.
	time.Sleep(10 * time.Millisecond)
	close(releaseLeader)

	timeout := 5 * time.Second
	if deadline, ok := t.Deadline(); ok {
		timeout = time.Until(deadline) - 500*time.Millisecond
	}

	var leader, follower result
	select {
	case leader = <-leaderResult:
	case <-time.After(timeout):
		t.Fatal("leader did not finish")
	}
	select {
	case follower = <-followerResult:
	case <-time.After(timeout):
		t.Fatal("follower did not finish")
	}

	if leader.err != nil {
		t.Errorf("leader: unexpected error: %v", leader.err)
	}
	if follower.err != nil {
		t.Errorf("follower: unexpected error: %v", follower.err)
	}
	if leader.tok == nil || leader.tok.AccessToken != "recovered-token" {
		t.Errorf("leader: expected recovered-token, got %v", leader.tok)
	}
	if follower.tok == nil || follower.tok.AccessToken != "recovered-token" {
		t.Errorf("follower: expected recovered-token, got %v", follower.tok)
	}

	// The load-bearing assertion: if Refresh used a per-caller key, the
	// follower's flight would have called Token() a second time.
	tokenSource.mu.Lock()
	calls := tokenSource.callCount
	tokenSource.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected exactly 1 tokenSource.Token() call (constant singleflight key), got %d", calls)
	}
}

// --- helpers for new tests ---

// timeoutNetError is a minimal net.Error with Timeout() == true.
type timeoutNetError struct{}

func (*timeoutNetError) Error() string   { return "i/o timeout" }
func (*timeoutNetError) Timeout() bool   { return true }
func (*timeoutNetError) Temporary() bool { return true }

var _ net.Error = (*timeoutNetError)(nil)

// fastBackOff returns a backoff with very short intervals so retry tests run quickly.
func fastBackOff() backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 10 * time.Millisecond
	b.MaxInterval = 50 * time.Millisecond
	b.Reset()
	return b
}

// --- error classification via background monitor ---

// TestMonitoredTokenSource_BackgroundMonitor_ErrorClassification verifies that the
// background monitor correctly distinguishes transient network errors (which trigger
// retries without marking the workload unauthenticated) from non-transient errors
// (which immediately mark the workload as unauthenticated and stop monitoring).
func TestMonitoredTokenSource_BackgroundMonitor_ErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		isTransient bool // true → monitor retries; false → monitor marks unauthenticated
	}{
		// Non-transient: plain errors and OAuth protocol failures (4xx with a
		// populated RFC 6749 error code) must fail fast.
		{name: "plain error", err: errors.New("some error"), isTransient: false},
		{name: "context.Canceled", err: context.Canceled, isTransient: false},
		{name: "context.DeadlineExceeded", err: context.DeadlineExceeded, isTransient: false},
		{name: "oauth2.RetrieveError 400 invalid_grant", err: createRetrieveErrorWithCode(http.StatusBadRequest, "invalid_grant", `{"error":"invalid_grant"}`), isTransient: false},
		{name: "oauth2.RetrieveError 401 invalid_client", err: createRetrieveErrorWithCode(http.StatusUnauthorized, "invalid_client", `{"error":"invalid_client"}`), isTransient: false},
		{name: "oauth2.RetrieveError 403 unauthorized_client", err: createRetrieveErrorWithCode(http.StatusForbidden, "unauthorized_client", `{"error":"unauthorized_client"}`), isTransient: false},
		{name: "oauth2.RetrieveError nil response", err: &oauth2.RetrieveError{}, isTransient: false},
		// Transient: network-level errors must be retried.
		{name: "*net.DNSError timeout", err: &net.DNSError{Err: "i/o timeout", Name: "example.com", IsTimeout: true}, isTransient: true},
		{name: "*net.OpError connection refused", err: &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}, isTransient: true},
		{name: "*url.Error wrapping *net.OpError", err: &url.Error{Op: "Post", URL: "https://example.com/token", Err: &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}}, isTransient: true},
		{name: "net.Error timeout", err: &timeoutNetError{}, isTransient: true},
		// Transient: OAuth server 5xx errors (load balancer, server restart).
		{name: "oauth2.RetrieveError 500", err: createRetrieveError(http.StatusInternalServerError, "Internal Server Error"), isTransient: true},
		{name: "oauth2.RetrieveError 502", err: createRetrieveError(http.StatusBadGateway, "Bad Gateway"), isTransient: true},
		{name: "oauth2.RetrieveError 503", err: createRetrieveError(http.StatusServiceUnavailable, "Service Unavailable"), isTransient: true},
		{name: "oauth2.RetrieveError 504", err: createRetrieveError(http.StatusGatewayTimeout, "Gateway Timeout"), isTransient: true},
		// Transient: 4xx without an RFC 6749 error code in the body.
		// These are infrastructure-level errors (WAF, CDN, proxy) that
		// commonly resolve on their own, not OAuth protocol failures.
		{name: "oauth2.RetrieveError 401 with HTML body", err: createRetrieveError(http.StatusUnauthorized, "<html><body>Unauthorized</body></html>"), isTransient: true},
		{name: "oauth2.RetrieveError 403 WAF block", err: createRetrieveError(http.StatusForbidden, "<html><body>Cloudflare Firewall Block</body></html>"), isTransient: true},
		{name: "oauth2.RetrieveError 400 with empty body", err: createRetrieveError(http.StatusBadRequest, ""), isTransient: true},
		{name: "oauth2.RetrieveError 408 request timeout", err: createRetrieveError(http.StatusRequestTimeout, ""), isTransient: true},
		// Transient: 429 Too Many Requests is retryable per HTTP standard
		// regardless of body content.
		{name: "oauth2.RetrieveError 429 empty body", err: createRetrieveError(http.StatusTooManyRequests, ""), isTransient: true},
		{name: "oauth2.RetrieveError 429 with rate-limit error code", err: createRetrieveErrorWithCode(http.StatusTooManyRequests, "rate_limit_exceeded", `{"error":"rate_limit_exceeded"}`), isTransient: true},
		// Transient: unparsable OAuth responses (HTML from load balancer on 200).
		{name: "oauth2 cannot parse json", err: fmt.Errorf("oauth2: cannot parse json: invalid character '<'"), isTransient: true},
		{name: "wrapped oauth2 parse error", err: fmt.Errorf("refresh failed: %w", fmt.Errorf("oauth2: cannot parse json: invalid character '<'")), isTransient: true},
		{name: "oauth2 cannot parse response", err: fmt.Errorf("oauth2: cannot parse response: invalid URL escape"), isTransient: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			tokenSource := newMockTokenSource()
			tokenSource.setTokenFn(func() (*oauth2.Token, error) {
				if tokenSource.callCount == 1 {
					// Initial tick: short-lived token so the monitor retries quickly.
					return &oauth2.Token{
						AccessToken: "initial-token",
						Expiry:      time.Now().Add(10 * time.Millisecond),
					}, nil
				}
				return nil, tt.err
			})

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if tt.isTransient {
				// Transient: SetWorkloadStatus must NOT be called — no EXPECT set.
				statusUpdater, _ := newMockStatusUpdater(ctrl)
				retrying := tokenSource.notifyOnCall(2)

				ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)
				ats.refresher.newBackOff = fastBackOff
				ats.StartBackgroundMonitoring()

				<-retrying // Ensure the retry loop has been entered before cancelling.
				cancel()
				<-ats.Stopped()
			} else {
				// Non-transient: SetWorkloadStatus must be called exactly once.
				statusUpdater, statusManager := newMockStatusUpdater(ctrl)
				statusManager.EXPECT().
					SetWorkloadStatus(
						gomock.Any(),
						"test-workload",
						rt.WorkloadStatusUnauthenticated,
						gomock.Any(),
					).
					Return(nil).
					Times(1)

				ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)
				ats.refresher.newBackOff = fastBackOff
				ats.StartBackgroundMonitoring()

				<-ats.Stopped() // Monitor stops itself after marking unauthenticated.
			}
		})
	}
}

// TestIsPermanentTokenEndpointError verifies that isPermanentTokenEndpointError
// is the strict inverse of classifyOAuthRetrieveError on the
// *oauth2.RetrieveError branch (with a non-nil Response). The DCR/CIMD
// remediation Warn fires only when the OAuth server returned a structured
// RFC 6749 error code; non-spec-compliant responses (HTML pages from a WAF,
// CDN, or reverse proxy) should not trigger that Warn because they carry no
// OAuth-protocol verdict.
//
// Existing Token() / markAsUnauthenticated tests reach this function through
// indirect call paths and yield 100% line coverage, but none of them assert
// on the boolean it returns. This test pins the behavioral contract directly.
func TestIsPermanentTokenEndpointError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         error
		isPermanent bool
	}{
		// Not an *oauth2.RetrieveError at all — no OAuth verdict to act on.
		{name: "plain error", err: errors.New("some error"), isPermanent: false},
		{name: "*oauth2.RetrieveError with nil Response", err: &oauth2.RetrieveError{}, isPermanent: false},
		// Transient HTTP-level conditions — never permanent.
		{name: "5xx server error", err: createRetrieveError(http.StatusInternalServerError, "Internal Server Error"), isPermanent: false},
		{name: "429 Too Many Requests", err: createRetrieveError(http.StatusTooManyRequests, ""), isPermanent: false},
		{name: "408 Request Timeout", err: createRetrieveError(http.StatusRequestTimeout, ""), isPermanent: false},
		// 4xx without an RFC 6749 error code — infrastructure response, no OAuth verdict.
		{name: "401 with HTML body (WAF)", err: createRetrieveError(http.StatusUnauthorized, "<html><body>Unauthorized</body></html>"), isPermanent: false},
		{name: "403 with HTML body (Cloudflare)", err: createRetrieveError(http.StatusForbidden, "<html><body>Firewall Block</body></html>"), isPermanent: false},
		// 4xx with an RFC 6749 error code — OAuth server rendered a verdict.
		{name: "400 invalid_grant", err: createRetrieveErrorWithCode(http.StatusBadRequest, "invalid_grant", `{"error":"invalid_grant"}`), isPermanent: true},
		{name: "401 invalid_client", err: createRetrieveErrorWithCode(http.StatusUnauthorized, "invalid_client", `{"error":"invalid_client"}`), isPermanent: true},
		{name: "403 unauthorized_client", err: createRetrieveErrorWithCode(http.StatusForbidden, "unauthorized_client", `{"error":"unauthorized_client"}`), isPermanent: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isPermanentTokenEndpointError(tt.err)
			if got != tt.isPermanent {
				t.Errorf("isPermanentTokenEndpointError(%v) = %v, want %v",
					tt.err, got, tt.isPermanent)
			}
		})
	}
}

// TestIsTransientNetworkError_AgainstRealOAuth2Library is a contract test,
// not a unit test of the package's own logic. It pins the assumption that
// isTransientRetrieveError relies on: that golang.org/x/oauth2 populates
// RetrieveError.ErrorCode iff the response carries a parseable RFC 6749
// 'error' field (whether JSON or form-encoded), and leaves ErrorCode empty
// for non-spec-compliant response shapes (HTML pages from a WAF, CDN, or
// reverse proxy).
//
// Cases here are deliberately limited to response shapes where the
// synthetic test helpers (createRetrieveError, createRetrieveErrorWithCode)
// could plausibly diverge from reality. Cases unambiguously covered by the
// synthetic table (clearly populated ErrorCode JSON, status-code-only
// branches like 5xx and 429) are intentionally not duplicated here.
func TestIsTransientNetworkError_AgainstRealOAuth2Library(t *testing.T) {
	t.Parallel()

	const refreshToken = "test-refresh-token"

	tests := []struct {
		name        string
		handler     http.HandlerFunc
		isTransient bool
	}{
		{
			// HTML 4xx is the canonical WAF/CDN block shape. Pinning that
			// the library leaves ErrorCode empty here is what underpins the
			// "infrastructure error" branch of isTransientRetrieveError.
			name: "403 with HTML body (Cloudflare WAF)",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte("<html><body>Cloudflare Firewall Block</body></html>"))
			},
			isTransient: true,
		},
		{
			name: "401 with HTML body",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("<html><body>Unauthorized</body></html>"))
			},
			isTransient: true,
		},
		{
			// Form-encoded error responses are non-spec but supported by
			// the library. Pinning that the library DOES populate ErrorCode
			// from form-encoded bodies — a synthetic helper used naively
			// (createRetrieveError without WithCode) would lie about this.
			name: "400 with form-encoded invalid_grant",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("error=invalid_grant&error_description=refresh+token+expired"))
			},
			isTransient: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(tt.handler)
			t.Cleanup(server.Close)

			cfg := &oauth2.Config{
				ClientID:     "test-client",
				ClientSecret: "test-secret",
				Endpoint:     oauth2.Endpoint{TokenURL: server.URL},
			}

			expired := &oauth2.Token{
				AccessToken:  "expired-access-token",
				RefreshToken: refreshToken,
				Expiry:       time.Now().Add(-time.Hour),
			}

			_, err := cfg.TokenSource(context.Background(), expired).Token()
			if err == nil {
				t.Fatalf("expected refresh to fail, got nil error")
			}

			got := isTransientNetworkError(err)
			if got != tt.isTransient {
				t.Errorf("isTransientNetworkError(%v) = %v, want %v",
					err, got, tt.isTransient)
			}
		})
	}
}

// --- background monitor transient-error behaviour ---

// TestMonitoredTokenSource_TransientErrorRetriesAndSucceeds verifies that when the
// background monitor encounters a transient network error it retries with backoff and,
// once the network recovers, does NOT mark the workload as unauthenticated.
func TestMonitoredTokenSource_TransientErrorRetriesAndSucceeds(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// No SetWorkloadStatus calls expected — the workload must stay authenticated.
	statusUpdater, _ := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		switch tokenSource.callCount {
		case 1:
			// Initial monitor kick: valid token that expires soon.
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		case 2, 3, 4:
			// Transient failures during the retry window.
			return nil, transientErr
		default:
			// Network recovered — return a long-lived token.
			return &oauth2.Token{
				AccessToken: "renewed-token",
				Expiry:      time.Now().Add(time.Hour),
			}, nil
		}
	})

	// Wait for call 5: the recovery token return.
	recovered := tokenSource.notifyOnCall(5)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)
	ats.refresher.newBackOff = fastBackOff
	ats.StartBackgroundMonitoring()

	// Block until the monitor has successfully recovered, then stop it.
	<-recovered
	cancel()
	<-ats.Stopped()
	// gomock verifies SetWorkloadStatus was NOT called (no EXPECT set).
}

// TestMonitoredTokenSource_TransientErrorContextCancellation verifies that cancelling
// the monitoring context while the retry loop is running does NOT mark the workload
// as unauthenticated (the workload was simply removed, not broken).
func TestMonitoredTokenSource_TransientErrorContextCancellation(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// No SetWorkloadStatus calls expected.
	statusUpdater, _ := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		// All subsequent calls: perpetual transient error.
		return nil, transientErr
	})

	// Wait for the first retry attempt before cancelling.
	retrying := tokenSource.notifyOnCall(2)

	ctx, cancel := context.WithCancel(context.Background())

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)
	ats.refresher.newBackOff = fastBackOff
	ats.StartBackgroundMonitoring()

	// Cancel once we know the retry loop is running, then wait for clean exit.
	<-retrying
	cancel()
	<-ats.Stopped()
	// gomock verifies SetWorkloadStatus was NOT called (no EXPECT set).
}

// TestMonitoredTokenSource_TransientThenNonTransientMarksUnauthenticated verifies that
// after a few retryable failures, a non-transient error (e.g. 401) stops the retry loop
// and marks the workload as unauthenticated exactly once.
func TestMonitoredTokenSource_TransientThenNonTransientMarksUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	statusManager.EXPECT().
		SetWorkloadStatus(
			gomock.Any(),
			"test-workload",
			rt.WorkloadStatusUnauthenticated,
			gomock.Any(),
		).
		Return(nil).
		Times(1)

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	nonTransientErr := createRetrieveErrorWithCode(http.StatusUnauthorized, "invalid_token", `{"error":"invalid_token"}`)

	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		switch tokenSource.callCount {
		case 1:
			// Initial tick: short-lived valid token.
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		case 2, 3:
			// Transient errors — retried.
			return nil, transientErr
		default:
			// Non-transient auth failure — must stop retrying and mark unauthenticated.
			return nil, nonTransientErr
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", "", "", statusUpdater)
	ats.refresher.newBackOff = fastBackOff
	ats.StartBackgroundMonitoring()

	// Monitor stops itself after the non-transient error; wait for that.
	<-ats.Stopped()
	// gomock verifies SetWorkloadStatus was called exactly once.
}

// --- AuthRetrying state machine ---

// drainShortRetryEnv sets env vars so the short retry inside transientRefresher
// exhausts in tens of milliseconds rather than minutes. Combined with
// fastBackOff on the refresher, the short-retry window becomes a no-op for
// AuthRetrying tests.
//
// Note: t.Setenv disallows the calling test from running with t.Parallel()
// because the env is process-wide. The AuthRetrying tests below therefore
// do not call t.Parallel().
func drainShortRetryEnv(t *testing.T) {
	t.Helper()
	t.Setenv(tokenRefreshMaxTriesEnv, "3")
	t.Setenv(tokenRefreshMaxElapsedTimeEnv, "200ms")
}

// TestMonitor_EnterAuthRetryingAfterShortRetryExhausts asserts that once the
// short-retry window inside transientRefresher exhausts on a still-transient
// error, the monitor transitions the workload to AuthRetrying and keeps the
// monitor goroutine alive (i.e. does NOT mark Unauthenticated).
func TestMonitor_EnterAuthRetryingAfterShortRetryExhausts(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "20ms")
	t.Setenv(authRetryingMaxElapsedEnv, "10s") // ample ceiling — must NOT be reached

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		return nil, transientErr
	})

	authRetryingCalled := make(chan struct{})
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
			close(authRetryingCalled)
			return nil
		}).
		Times(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-authRetryingCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("did not transition to AuthRetrying within 2s")
	}

	// Monitor must remain alive after entering AuthRetrying.
	time.Sleep(80 * time.Millisecond)
	select {
	case <-ats.Stopped():
		t.Fatal("monitor stopped after AuthRetrying; expected it to stay alive")
	default:
	}
	if !ats.inAuthRetrying() {
		t.Fatal("expected inAuthRetrying() to be true")
	}

	cancel()
	<-ats.Stopped()
}

// TestMonitor_AuthRetryingRecoversToRunning asserts that when the token
// endpoint recovers during AuthRetrying, the next successful refresh transitions
// the workload back to Running and clears the cached transient state.
func TestMonitor_AuthRetryingRecoversToRunning(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "20ms")
	t.Setenv(authRetryingMaxElapsedEnv, "10s")

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	recovered := make(chan struct{})
	var once sync.Once
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		// Recover once the AuthRetrying transition has been observed and a
		// post-transition tick fires.
		select {
		case <-recovered:
			return &oauth2.Token{
				AccessToken: "recovered-token",
				Expiry:      time.Now().Add(time.Hour),
			}, nil
		default:
			return nil, transientErr
		}
	})

	runningCalled := make(chan struct{})
	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
				once.Do(func() { close(recovered) })
				return nil
			}).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusRunning, "").
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
				close(runningCalled)
				return nil
			}).
			Times(1),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-runningCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("did not recover to Running within 2s")
	}
	if ats.inAuthRetrying() {
		t.Fatal("expected inAuthRetrying() to be false after recovery")
	}

	cancel()
	<-ats.Stopped()
}

// TestMonitor_AuthRetryingCeilingTransitionsToUnauthenticated asserts that
// after the configured ceiling elapses while still in AuthRetrying, the
// monitor gives up and marks the workload Unauthenticated.
func TestMonitor_AuthRetryingCeilingTransitionsToUnauthenticated(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "20ms")
	// Ceiling = 200ms (vs e.g. 50ms): leaves enough margin that the
	// SetWorkloadStatus(AuthRetrying) emit + the first post-entry tick
	// don't accidentally race the ceiling check on a slow runner.
	t.Setenv(authRetryingMaxElapsedEnv, "200ms")

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		return nil, transientErr
	})

	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			Return(nil).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, reason string) error {
				if !strings.Contains(reason, "transiently for over") {
					t.Errorf("expected ceiling-specific reason; got %q", reason)
				}
				return nil
			}).
			Times(1),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-ats.Stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop after AuthRetrying ceiling exceeded")
	}
}

// TestToken_HotCallerFastFailsDuringAuthRetrying asserts that a hot
// caller (see MonitoredTokenSource type doc) calling Token() during the
// AuthRetrying window gets the cached error immediately, without
// re-entering the short-retry loop against the still-broken endpoint.
func TestToken_HotCallerFastFailsDuringAuthRetrying(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "500ms") // long gap so the hot call sits inside it
	t.Setenv(authRetryingMaxElapsedEnv, "10s")

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		return nil, transientErr
	})

	authRetryingCalled := make(chan struct{})
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
			close(authRetryingCalled)
			return nil
		}).
		Times(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-authRetryingCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("did not enter AuthRetrying within 2s")
	}

	// Snapshot underlying source's call count, then make a hot call.
	tokenSource.mu.Lock()
	beforeCount := tokenSource.callCount
	tokenSource.mu.Unlock()

	start := time.Now()
	_, hotErr := ats.Token()
	elapsed := time.Since(start)

	if hotErr == nil {
		t.Fatal("expected Token() to fail-fast during AuthRetrying")
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("Token() took %v, expected fast-fail (<100ms)", elapsed)
	}
	if !errors.Is(hotErr, transientErr) {
		t.Errorf("expected Token() error to wrap transientErr; got %v", hotErr)
	}

	tokenSource.mu.Lock()
	afterCount := tokenSource.callCount
	tokenSource.mu.Unlock()
	if afterCount > beforeCount {
		t.Errorf("Token() invoked underlying source during AuthRetrying (calls %d → %d)", beforeCount, afterCount)
	}

	cancel()
	<-ats.Stopped()
}

// TestMonitor_PermanentErrorDuringAuthRetryingTickGivesUpImmediately asserts
// that a permanent OAuth error during a post-AuthRetrying tick stops the
// monitor immediately, without waiting for the ceiling.
func TestMonitor_PermanentErrorDuringAuthRetryingTickGivesUpImmediately(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "20ms")
	t.Setenv(authRetryingMaxElapsedEnv, "10s") // ample ceiling — must NOT be reached

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	permanentErr := createRetrieveErrorWithCode(http.StatusBadRequest, "invalid_grant", `{"error":"invalid_grant"}`)

	authRetryingCalled := make(chan struct{})
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		select {
		case <-authRetryingCalled:
			return nil, permanentErr
		default:
			return nil, transientErr
		}
	})

	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
				close(authRetryingCalled)
				return nil
			}).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, reason string) error {
				if !strings.Contains(reason, "invalid_grant") {
					t.Errorf("expected unauthenticated reason to mention invalid_grant; got %q", reason)
				}
				return nil
			}).
			Times(1),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-ats.Stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not transition Unauthenticated within 2s")
	}
}

// TestMonitor_DCRWarnEmittedAfterZeroExpiryExit asserts that the DCR/CIMD
// remediation Warn still fires for a permanent 4xx even when an earlier
// zero-expiry exit in onTick has already closed stopMonitoring. The Warn
// gate (dcrWarnOnce) must be independent of the channel-close gate
// (stopOnce); otherwise the zero-expiry path silently suppresses the
// remediation hint for the first real Unauthenticated transition.
//
// Not run in parallel: slog.SetDefault is process-wide.
//
//nolint:paralleltest // mutates slog default, mirrors TestMonitor_DCRWarnSilentOnCeilingGiveUp
func TestMonitor_DCRWarnEmittedAfterZeroExpiryExit(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)

	// Capture slog output (see TestMonitor_DCRWarnSilentOnCeilingGiveUp).
	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// Return a token with zero Expiry on the first call so onTick hits the
	// zero-expiry exit path and consumes the stopOnce slot via
	// stopMonitoringOnce(). The monitor must not emit Unauthenticated for
	// this exit — the current token is valid, just unschedulable.
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return &oauth2.Token{AccessToken: "no-expiry-token"}, nil
	})

	// Only the later explicit markAsUnauthenticated call should produce a
	// status update; the zero-expiry exit must not.
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload",
		"https://issuer.example.com", "test-client-id", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	// Wait for the monitor to exit via the zero-expiry path.
	select {
	case <-ats.Stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop within 2s after zero-expiry token")
	}

	// Now simulate a real permanent 4xx transition. The DCR Warn must fire
	// even though stopOnce has already been consumed by the zero-expiry exit.
	ats.markAsUnauthenticated("simulated permanent 4xx", true)

	if !strings.Contains(logBuf.String(), "delete the cached credentials") {
		t.Errorf("DCR remediation Warn did not fire after zero-expiry exit;"+
			" expected substring \"delete the cached credentials\" in slog output:\n%s",
			logBuf.String())
	}
}

// TestMonitor_DCRWarnSilentOnCeilingGiveUp asserts that when the monitor gives
// up at the AuthRetrying ceiling, the DCR/CIMD remediation warning does NOT
// fire — a transient ceiling is not a "stale cached credentials" signal.
//
// Not run in parallel: slog.SetDefault is process-wide.
func TestMonitor_DCRWarnSilentOnCeilingGiveUp(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "20ms")
	t.Setenv(authRetryingMaxElapsedEnv, "200ms")

	// Capture slog output by swapping the default logger for the lifetime
	// of this test. bytes.Buffer is not goroutine-safe, but the test reads
	// it only after <-ats.Stopped() — by which point the monitor goroutine
	// has exited and no further writes occur.
	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		return nil, transientErr
	})

	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
		Return(nil).
		Times(1)
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Construct with a non-empty client_id so the DCR Warn *could* fire on a
	// permanent-classified error; we're verifying it doesn't fire on a
	// transient-classified ceiling give-up.
	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload",
		"https://issuer.example.com", "client-123", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-ats.Stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop within 2s")
	}

	if strings.Contains(logBuf.String(), "delete the cached credentials") {
		t.Errorf("DCR remediation Warn fired on transient ceiling give-up; output:\n%s", logBuf.String())
	}
}

// TestMonitor_DCRWarnEmittedAfterCeilingThenPermanentError asserts that the
// DCR/CIMD remediation Warn still fires for a permanent 4xx that follows a
// ceiling-triggered Unauthenticated transition. The ceiling path invokes
// markAsUnauthenticated with permanent4xx=false; if the dcrWarnOnce gate is
// placed inside the Do lambda (rather than around it), that first invocation
// would still enter Do, early-return, and consume the once-slot — silently
// suppressing the Warn on the later legitimate permanent 4xx Token() error.
//
// Not run in parallel: slog.SetDefault is process-wide.
//
//nolint:paralleltest // mutates slog default, mirrors TestMonitor_DCRWarnEmittedAfterZeroExpiryExit
func TestMonitor_DCRWarnEmittedAfterCeilingThenPermanentError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "20ms")
	t.Setenv(authRetryingMaxElapsedEnv, "200ms")

	// Capture slog output (see TestMonitor_DCRWarnEmittedAfterZeroExpiryExit).
	var logBuf bytes.Buffer
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// First call: succeed with a near-immediate expiry so the monitor wakes
	// up quickly. Subsequent calls: transient connect refusal, driving the
	// monitor through AuthRetrying and eventually the ceiling.
	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return &oauth2.Token{
				AccessToken: "initial-token",
				Expiry:      time.Now().Add(10 * time.Millisecond),
			}, nil
		}
		return nil, transientErr
	})

	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			Return(nil).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
			Return(nil).
			// First ceiling-triggered Unauthenticated; second is the explicit
			// permanent-4xx markAsUnauthenticated call below.
			Times(2),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Non-empty client_id so the DCR Warn is eligible to fire on a
	// permanent-classified error.
	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload",
		"https://issuer.example.com", "client-123", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	// Wait for the monitor to exit via the ceiling path.
	select {
	case <-ats.Stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop within 2s after AuthRetrying ceiling")
	}

	// Sanity-check: the ceiling-triggered Unauthenticated must NOT have
	// emitted the DCR Warn (it's transient by definition).
	if strings.Contains(logBuf.String(), "delete the cached credentials") {
		t.Fatalf("DCR remediation Warn fired on ceiling-triggered Unauthenticated;"+
			" output:\n%s", logBuf.String())
	}

	// Now simulate a follow-up Token() permanent 4xx. The DCR Warn MUST
	// fire here — the ceiling path's markAsUnauthenticated call (with
	// permanent4xx=false) must not have consumed the dcrWarnOnce slot.
	ats.markAsUnauthenticated("simulated permanent 4xx", true)

	if !strings.Contains(logBuf.String(), "delete the cached credentials") {
		t.Errorf("DCR remediation Warn did not fire after ceiling-then-permanent;"+
			" expected substring \"delete the cached credentials\" in slog output:\n%s",
			logBuf.String())
	}
}

// --- end-to-end integration tests against a real HTTP OAuth server ---
//
// The tests above use a mock oauth2.TokenSource (newMockTokenSource) and
// drive the state machine via synthetic errors. The tests below wire the
// real golang.org/x/oauth2 ReuseTokenSource against the scriptable
// oauthtest.ControllableServer so that errors are produced by the OAuth
// library parsing an actual HTTP response.

// TestIntegration_AuthRetryingRecoversAfterRealWAFBlock drives the full
// state machine end-to-end against a real OAuth HTTP server: initial
// success → real WAF-style 403 HTML refresh failure → AuthRetrying →
// flip server back to success → Running. Unlike the mock-based tests
// above, this exercises the actual golang.org/x/oauth2 response parsing
// and isTransientNetworkError classification of *oauth2.RetrieveError
// values constructed by the library from real HTTP responses.
func TestIntegration_AuthRetryingRecoversAfterRealWAFBlock(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "100ms")
	t.Setenv(authRetryingMaxElapsedEnv, "30s")

	srv := oauthtest.NewControllableServer()
	defer srv.Close()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)

	authRetryingCalled := make(chan struct{})
	runningCalled := make(chan struct{})

	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
				close(authRetryingCalled)
				return nil
			}).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusRunning, "").
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, _ string) error {
				close(runningCalled)
				return nil
			}).
			Times(1),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, oauthtest.NewRealTokenSource(srv.URL),
		"test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	// Give the monitor a moment to do its first successful refresh.
	initialRefreshDeadline := time.Now().Add(2 * time.Second)
	for srv.RequestCount() < 1 && time.Now().Before(initialRefreshDeadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.RequestCount() < 1 {
		t.Fatal("monitor did not issue an initial refresh against the fake server")
	}

	// Flip to WAF block. Next monitor tick should exhaust short retry
	// and transition to AuthRetrying.
	srv.SetMode(oauthtest.ModeWAFBlock)
	select {
	case <-authRetryingCalled:
	case <-time.After(10 * time.Second):
		t.Fatalf("did not transition to AuthRetrying within 10s; server saw %d requests", srv.RequestCount())
	}

	// Recover. Next AuthRetrying tick should refresh successfully and
	// transition back to Running.
	srv.SetMode(oauthtest.ModeSuccess)
	select {
	case <-runningCalled:
	case <-time.After(10 * time.Second):
		t.Fatalf("did not recover to Running within 10s; server saw %d requests", srv.RequestCount())
	}
	if ats.inAuthRetrying() {
		t.Error("expected inAuthRetrying() to be false after recovery")
	}

	cancel()
	<-ats.Stopped()
}

// TestIntegration_AuthRetryingCeilingThroughRealOAuthServer drives the
// monitor through a complete ceiling timeout end-to-end against a real
// OAuth server: real WAF block persists → AuthRetrying → ceiling
// exceeded → Unauthenticated. The ceiling here is intentionally tight
// (200ms) so the test completes quickly; in production the default is
// 24h.
func TestIntegration_AuthRetryingCeilingThroughRealOAuthServer(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	t.Setenv(authRetryingTickIntervalEnv, "50ms")
	t.Setenv(authRetryingMaxElapsedEnv, "200ms")

	srv := oauthtest.NewControllableServer()
	defer srv.Close()
	// Start in WAF-block mode so the first refresh attempt fails.
	srv.SetMode(oauthtest.ModeWAFBlock)

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			Return(nil).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
			DoAndReturn(func(_ context.Context, _ string, _ rt.WorkloadStatus, reason string) error {
				if !strings.Contains(reason, "transiently for over") {
					t.Errorf("expected ceiling-specific reason; got %q", reason)
				}
				return nil
			}).
			Times(1),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, oauthtest.NewRealTokenSource(srv.URL),
		"test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	select {
	case <-ats.Stopped():
	case <-time.After(5 * time.Second):
		t.Fatalf("monitor did not reach ceiling within 5s; server saw %d requests", srv.RequestCount())
	}
}

// TestToken_DoesNotEnterAuthRetryingAfterMonitorStopped asserts the
// stopMonitoring gate in enterAuthRetrying: once a permanent error has
// closed the monitor, a subsequent hot caller observing transient errors
// must NOT transition the workload back into AuthRetrying. Without the
// gate, the workload would be stuck at AuthRetrying with no monitor alive
// to honor the ceiling or drive recovery.
func TestToken_DoesNotEnterAuthRetryingAfterMonitorStopped(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	// Tick interval and ceiling don't matter for this test — the monitor
	// never runs. Set them so any spurious test path completes quickly.
	t.Setenv(authRetryingTickIntervalEnv, "10s")
	t.Setenv(authRetryingMaxElapsedEnv, "10s")

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	permanentErr := createRetrieveErrorWithCode(http.StatusBadRequest, "invalid_grant", `{"error":"invalid_grant"}`)
	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}

	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		if tokenSource.callCount == 1 {
			return nil, permanentErr
		}
		return nil, transientErr
	})

	// Expect exactly one Unauthenticated transition. Crucially, no
	// AuthRetrying transition is allowed — gomock fails the test if one
	// fires unexpectedly.
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)

	// Drive the workload to Unauthenticated via a hot-caller call.
	if _, err := ats.Token(); err == nil {
		t.Fatal("expected permanent error from initial Token() call")
	}

	// Subsequent hot-caller calls observing transient errors must NOT
	// transition back to AuthRetrying, and must NOT pay the full short-
	// retry duration against the broken endpoint. Without the
	// stopMonitoring gate in Token(), each call would spend ~50ms+ in
	// refresher.Refresh before returning.
	for i := 0; i < 3; i++ {
		start := time.Now()
		if _, err := ats.Token(); err == nil {
			t.Fatalf("hot-caller call %d unexpectedly succeeded", i)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Millisecond {
			t.Errorf("hot-caller call %d took %v after monitor stopped; "+
				"expected fast return (<5ms) via stopMonitoring gate, "+
				"not a full short-retry pass", i, elapsed)
		}
	}

	if ats.inAuthRetrying() {
		t.Error("workload entered AuthRetrying after monitor stopped; gate failed")
	}
}

// TestConcurrent_EnterAuthRetryingAndMarkAsUnauthenticated verifies the
// in-memory invariant that after markAsUnauthenticated completes
// concurrently with enterAuthRetrying, transientStartedAt and
// lastTransientErr are always cleared. Without the gate-under-mu
// ordering in enterAuthRetrying, a hostile interleaving (gate check
// before lock acquisition) allows enterAuthRetrying to re-populate the
// fields *after* markAsUnauthenticated cleared them, which would leave
// hot callers stuck fast-failing against a dead monitor via
// fastFailIfAuthRetrying (which reads the fields, not the channel).
func TestConcurrent_EnterAuthRetryingAndMarkAsUnauthenticated(t *testing.T) {
	t.Parallel()
	// 1000 iterations to give the runtime scheduler enough chances to
	// interleave the two goroutines unfavourably across runs.
	const iterations = 1000
	for i := 0; i < iterations; i++ {
		runConcurrentEnterAndMark(t, i)
	}
}

func runConcurrentEnterAndMark(t *testing.T, iter int) {
	t.Helper()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// Either status transition may or may not fire depending on which
	// goroutine wins the gate check; we are testing the in-memory
	// invariant, not the disk-write order.
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil).
		AnyTimes()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Skip StartBackgroundMonitoring — we drive both callers ourselves.
	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		ats.enterAuthRetrying(errors.New("transient"))
	}()
	go func() {
		defer wg.Done()
		ats.markAsUnauthenticated("permanent error", false)
	}()
	wg.Wait()

	ats.mu.Lock()
	startedAt := ats.transientStartedAt
	lastErr := ats.lastTransientErr
	ats.mu.Unlock()

	if !startedAt.IsZero() {
		t.Fatalf("iter %d: transientStartedAt should be zero after markAsUnauthenticated, got %v", iter, startedAt)
	}
	if lastErr != nil {
		t.Fatalf("iter %d: lastTransientErr should be nil after markAsUnauthenticated, got %v", iter, lastErr)
	}
	// Guard against a future regression that drops the stopMonitoring
	// close in markAsUnauthenticated: the in-memory invariant above
	// depends on the channel being closed, so a regression there would
	// silently break the gate check in enterAuthRetrying.
	select {
	case <-ats.stopMonitoring:
	default:
		t.Fatalf("iter %d: stopMonitoring should be closed after markAsUnauthenticated", iter)
	}
}

// TestMonitor_ZeroExpiryTokenClosesStopMonitoring asserts that when the
// monitor exits because the token source returned a token with a zero
// Expiry, it closes stopMonitoring (not only stopped). The Token() hot
// path keys its short-circuit gate off stopMonitoring; if only stopped
// is closed, a later transient error would burn the full short-retry
// window before enterAuthRetrying's gate (under mu) finally aborted,
// leaving the workload in AuthRetrying with no monitor to drive the
// ceiling.
func TestMonitor_ZeroExpiryTokenClosesStopMonitoring(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusUpdater, _ := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	// First call returns a token with zero expiry, which drives the
	// monitor to exit cleanly without emitting any status transition.
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return &oauth2.Token{
			AccessToken: "no-expiry-token",
			Expiry:      time.Time{},
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	ats.StartBackgroundMonitoring()

	// Wait for the monitor to exit.
	select {
	case <-ats.Stopped():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not exit within 2s after zero-expiry token")
	}

	// The stopMonitoring gate must be closed too — otherwise Token()'s
	// hot-path short-circuit (which selects on stopMonitoring) would
	// continue admitting callers into the short-retry loop against a
	// dead monitor.
	select {
	case <-ats.stopMonitoring:
	default:
		t.Fatal("monitor exited via zero-expiry path but stopMonitoring is still open; " +
			"Token() hot path would burn the short-retry window on next transient error")
	}
}

// TestMonitor_AuthRetryingClearedOnRefreshRecovery asserts that when a
// hot caller enters AuthRetrying between a tick's inAuthRetrying() check
// and the same tick's refresher.Refresh succeeding, the monitor exits
// AuthRetrying on the success path so subsequent hot Token() calls
// resume serving real tokens instead of fast-failing with the stale
// cached transient error.
//
// Without exitAuthRetrying() on the second success path, the workload
// would sit in AuthRetrying with no scheduled monitor tick for the
// configured cadence (default 10min) — every hot Token() call in that
// window would fast-fail with the cached transient error even though
// the upstream is healthy and the monitor itself just observed a
// successful refresh.
func TestMonitor_AuthRetryingClearedOnRefreshRecovery(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	drainShortRetryEnv(t)
	// Long tick interval: the test drives the recovery via the same
	// tick that the hot caller raced; no further ticks should be
	// needed for the assertion to hold.
	t.Setenv(authRetryingTickIntervalEnv, "10s")
	t.Setenv(authRetryingMaxElapsedEnv, "10s")

	statusUpdater, statusManager := newMockStatusUpdater(ctrl)
	tokenSource := newMockTokenSource()

	transientErr := &net.OpError{Op: "dial", Net: "tcp", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}}

	// One AuthRetrying transition (from the hot caller staged inside
	// the token source callback) and one Running transition (from the
	// monitor's success-path exit — the behaviour under test).
	gomock.InOrder(
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusAuthRetrying, gomock.Any()).
			Return(nil).
			Times(1),
		statusManager.EXPECT().
			SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusRunning, "").
			Return(nil).
			Times(1),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := newMonitoredTokenSourceWithBackOff(ctx, tokenSource, "test-workload", "", "", statusUpdater, fastBackOff)
	// Do NOT call StartBackgroundMonitoring — drive onTick directly so
	// we can stage the race precisely.

	// Stage the race:
	//  - call 1 (onTick's raw direct call): transient error. onTick has
	//    already passed its inAuthRetrying() == false check, so it falls
	//    through to refresher.Refresh.
	//  - call 2 (refresher.Refresh's underlying call): simulate the
	//    hot-caller race by calling enterAuthRetrying right before
	//    returning success. This mirrors a real hot Token() that
	//    exhausted its own short retry and called enterAuthRetrying
	//    while the monitor's Refresh was in flight.
	var rawCalls int
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		rawCalls++
		switch rawCalls {
		case 1:
			return nil, transientErr
		case 2:
			// Stage the race: a hot caller wedged the workload into
			// AuthRetrying after onTick passed its check but before
			// Refresh returns.
			ats.enterAuthRetrying(transientErr)
			return &oauth2.Token{
				AccessToken: "recovered-token",
				Expiry:      time.Now().Add(time.Hour),
			}, nil
		default:
			return &oauth2.Token{
				AccessToken: "post-recovery-token",
				Expiry:      time.Now().Add(time.Hour),
			}, nil
		}
	})

	shouldStop, _ := ats.onTick()
	if shouldStop {
		t.Fatal("monitor wanted to stop after a successful Refresh; expected continue")
	}

	// The second success path must have cleared AuthRetrying so
	// subsequent hot callers don't keep fast-failing with the stale
	// cached error.
	if ats.inAuthRetrying() {
		t.Fatal("monitor exited Refresh successfully but workload still in AuthRetrying; " +
			"subsequent hot Token() calls will keep fast-failing on the stale cached error")
	}

	// Belt-and-braces: a hot Token() call must NOT fast-fail with the
	// cached transient error now that AuthRetrying is cleared.
	tok, err := ats.Token()
	if err != nil {
		t.Fatalf("hot Token() after recovery returned error: %v", err)
	}
	if tok == nil || tok.AccessToken != "post-recovery-token" {
		t.Fatalf("hot Token() after recovery returned unexpected token: %+v", tok)
	}
}
