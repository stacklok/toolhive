package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
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

func (m *mockTokenSource) Token() (*oauth2.Token, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return m.tokenFn()
}

// createRetrieveError creates an error for testing token failures
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

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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
	retrieveErr := createRetrieveError(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token expired"}`)
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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
		retrieveErr := createRetrieveError(http.StatusUnauthorized, `{"error":"invalid_token"}`)
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)
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

	retrieveErr := createRetrieveError(http.StatusUnauthorized, `{"error":"invalid_token"}`)
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewMonitoredTokenSource(ctx, tokenSource, "test-workload", statusUpdater)

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
