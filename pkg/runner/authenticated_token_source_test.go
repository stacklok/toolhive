package runner

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

// createRetrieveError creates an oauth2.RetrieveError for testing
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

func TestAuthenticatedTokenSource_SuccessfulTokenRetrieval(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
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

	// Create authenticated token source
	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

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

func TestAuthenticatedTokenSource_AuthenticationErrorMarksUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	// Create a RetrieveError with invalid_grant in body
	retrieveErr := createRetrieveError(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"refresh token expired"}`)
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

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

func TestAuthenticatedTokenSource_NonRetrieveErrorDoesNotMarkUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	// Generic error (not oauth2.RetrieveError) should not mark as unauthenticated
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, errors.New("some generic error")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

	// Should NOT call SetWorkloadStatus for non-RetrieveError
	// (no expectations set means we expect it not to be called)

	// Token retrieval should fail but NOT mark as unauthenticated
	_, err := ats.Token()
	if err == nil {
		t.Fatal("Expected error, got nil")
	}

	time.Sleep(50 * time.Millisecond)
}

func TestAuthenticatedTokenSource_BackgroundMonitoring(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
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

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

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

	ats.startBackgroundMonitoring()

	// Wait for token to expire and background monitoring to detect failure
	// The timer is scheduled for when token expires (500ms), then it processes the error
	// Need enough time for: initial timer (1ms) + token expiry (500ms) + error processing
	// Using a longer wait with a polling approach for more reliable test
	maxWait := 2 * time.Second
	checkInterval := 50 * time.Millisecond
	elapsed := time.Duration(0)

	for elapsed < maxWait {
		select {
		case <-ats.stopMonitoring:
			// Good, monitoring stopped
			return
		case <-time.After(checkInterval):
			elapsed += checkInterval
		}
	}

	t.Error("Expected monitoring to stop after authentication error, but it's still running after 2 seconds")
}

func TestAuthenticatedTokenSource_TransientErrorRetryWithBackoff(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	callCount := 0
	// Use a generic error (not oauth2.RetrieveError) - should be treated as transient
	genericErr := errors.New("network timeout")
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		callCount++
		if callCount < 3 {
			// First few calls: transient error
			return nil, genericErr
		}
		// After retries: return valid token
		return &oauth2.Token{
			AccessToken:  "recovered-token",
			RefreshToken: "refresh",
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

	// Should NOT call SetWorkloadStatus for transient errors
	// (no expectations set means we expect it not to be called)

	ats.startBackgroundMonitoring()

	// Wait for retries and recovery
	time.Sleep(2 * time.Second)

	// Verify monitoring is still active (should recover and continue)
	select {
	case <-ats.stopMonitoring:
		t.Error("Expected monitoring to continue after transient errors, but it stopped")
	default:
		// Good, monitoring is still active
	}

	cancel() // Clean up
}

func TestAuthenticatedTokenSource_ExpiredTokenHandling(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
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

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

	// Should not mark as unauthenticated just for expired token
	// (oauth2 library should handle refresh; we only mark on actual auth errors)
	// (no expectations set means we expect SetWorkloadStatus not to be called)

	ats.startBackgroundMonitoring()

	// Wait a bit for monitoring to check
	time.Sleep(200 * time.Millisecond)

	cancel()
}

func TestAuthenticatedTokenSource_StopMonitoring(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
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

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)
	ats.startBackgroundMonitoring()

	// Wait a bit to ensure monitoring started
	time.Sleep(100 * time.Millisecond)

	// Stop monitoring via context cancellation
	cancel()

	// Wait a bit for monitoring to stop
	time.Sleep(100 * time.Millisecond)

	// Verify monitoring stopped (check context is done, channel may or may not be closed)
	select {
	case <-ats.monitoringCtx.Done():
		// Good, context is cancelled
	default:
		t.Error("Expected monitoring context to be cancelled")
	}
}

func TestAuthenticatedTokenSource_MultipleCallsToToken(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	retrieveErr := createRetrieveError(http.StatusUnauthorized, `{"error":"invalid_token"}`)
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, retrieveErr
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ats := NewAuthenticatedTokenSource(ctx, tokenSource, "test-workload", statusManager)

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

func TestIsAuthenticationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "oauth2.RetrieveError with 401",
			err:  createRetrieveError(http.StatusUnauthorized, `{"error":"invalid_token"}`),
			want: true,
		},
		{
			name: "oauth2.RetrieveError with 400",
			err:  createRetrieveError(http.StatusBadRequest, `{"error":"invalid_grant"}`),
			want: true,
		},
		{
			name: "oauth2.RetrieveError with invalid_grant in body",
			err:  createRetrieveError(http.StatusOK, `{"error":"invalid_grant"}`),
			want: true,
		},
		{
			name: "oauth2.RetrieveError with invalid_client in body",
			err:  createRetrieveError(http.StatusOK, `{"error":"invalid_client"}`),
			want: true,
		},
		{
			name: "oauth2.RetrieveError with invalid_token in body",
			err:  createRetrieveError(http.StatusOK, `{"error":"invalid_token"}`),
			want: true,
		},
		{
			name: "oauth2.RetrieveError with 500 (not auth error)",
			err:  createRetrieveError(http.StatusInternalServerError, `{"error":"server_error"}`),
			want: false,
		},
		{
			name: "oauth2.RetrieveError with 404 (not auth error)",
			err:  createRetrieveError(http.StatusNotFound, `{"error":"not_found"}`),
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "generic error",
			err:  errors.New("some generic error"),
			want: false,
		},
		{
			name: "error with oauth in message but not RetrieveError",
			err:  errors.New("oauth connection failed"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isAuthenticationError(tt.err)
			if got != tt.want {
				t.Errorf("isAuthenticationError() = %v, want %v", got, tt.want)
			}
		})
	}
}
