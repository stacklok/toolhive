package runner

import (
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
	"golang.org/x/oauth2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
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

	// Create authenticated token source
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Test successful token retrieval
	token, err := ats.Token()
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if token.AccessToken != "test-access-token" {
		t.Errorf("Expected access token 'test-access-token', got %s", token.AccessToken)
	}
}

func TestAuthenticatedTokenSource_TokenErrorMarksUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, errors.New("token error")
	})

	// GetWorkload will be called by background monitoring and by Token() method
	// Background monitoring calls it once, then Token() method calls it once
	statusManager.EXPECT().
		GetWorkload(gomock.Any(), "test-workload").
		Return(core.Workload{}, errors.New("workload not found")).
		Times(2) // Background check + manual Token() call

	// Expect SetWorkloadStatus to be called when token retrieval fails (twice: background + manual)
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(2) // Background check + manual Token() call

	// Create authenticated token source (starts background monitoring)
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Wait a bit for background monitoring to complete
	time.Sleep(50 * time.Millisecond)

	// Token retrieval should fail and mark as unauthenticated
	_, err := ats.Token()
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestAuthenticatedTokenSource_TokenErrorWithExistingUnauthenticatedStatus(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, errors.New("token error")
	})

	// GetWorkload returns unauthenticated status (for both background check and manual call)
	statusManager.EXPECT().
		GetWorkload(gomock.Any(), "test-workload").
		Return(core.Workload{
			Name:   "test-workload",
			Status: rt.WorkloadStatusUnauthenticated,
		}, nil).
		Times(2) // Background check + manual Token() call

	// Should NOT call SetWorkloadStatus since already unauthenticated
	// (no expectation set, which means we expect it NOT to be called)

	// Create authenticated token source (starts background monitoring)
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Wait a bit for background monitoring to complete
	time.Sleep(50 * time.Millisecond)

	// Token retrieval should fail but NOT mark again (already unauthenticated)
	_, err := ats.Token()
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
}

func TestAuthenticatedTokenSource_ExpiredTokenWithoutRefresh(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	// When token is expired, checkTokenValidity directly calls markAsUnauthenticated
	// without checking GetWorkload first
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	expiredToken := &oauth2.Token{
		AccessToken:  "test-access-token",
		RefreshToken: "",                         // No refresh token
		Expiry:       time.Now().Add(-time.Hour), // Expired
	}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return expiredToken, nil
	})

	// Create authenticated token source (starts background monitoring)
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Wait for background monitoring to detect the failure and update status
	time.Sleep(200 * time.Millisecond)
}

func TestAuthenticatedTokenSource_ImmediateAuthFailure(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	// GetWorkload returns workload not found initially
	statusManager.EXPECT().
		GetWorkload(gomock.Any(), "test-workload").
		Return(core.Workload{}, errors.New("workload not found")).
		Times(1)

	// Expect SetWorkloadStatus to be called
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return nil, errors.New("invalid_grant: refresh token has expired")
	})

	// Create authenticated token source (starts background monitoring)
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Wait for background monitoring to detect the failure and update status
	time.Sleep(200 * time.Millisecond)
}

func TestAuthenticatedTokenSource_RefreshTokenExpiry(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	// When token is expired, checkTokenValidity directly calls markAsUnauthenticated
	// without checking GetWorkload first
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	expiredToken := &oauth2.Token{
		AccessToken:  "expired-access-token",
		RefreshToken: "expired-refresh-token",
		Expiry:       time.Now().Add(-time.Hour), // Expired 1 hour ago
	}
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		return expiredToken, nil
	})

	// Create authenticated token source (starts background monitoring)
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Wait for background monitoring to detect the failure and update status
	time.Sleep(200 * time.Millisecond)
}

// TestAuthenticatedTokenSource_ExpiryTriggersUnauthenticated verifies that when a valid token
// expires and the next Token() call fails to refresh, the workload is marked unauthenticated
// without long waits. This avoids needing to wait an hour for real expiry.
func TestAuthenticatedTokenSource_ExpiryTriggersUnauthenticated(t *testing.T) {
	t.Parallel()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	statusManager := statusMocks.NewMockStatusManager(ctrl)
	tokenSource := newMockTokenSource()

	// First call returns valid short-lived token
	shortLived := &oauth2.Token{
		AccessToken:  "short-lived",
		RefreshToken: "rt",
		Expiry:       time.Now().Add(1 * time.Second),
	}

	callCount := 0
	tokenSource.setTokenFn(func() (*oauth2.Token, error) {
		callCount++
		if callCount == 1 {
			// First call: return valid short-lived token
			return shortLived, nil
		}
		// Subsequent calls: simulate failed refresh after expiry
		return nil, errors.New("invalid_grant: refresh token has expired")
	})

	// After expiry check, GetWorkload will be called
	statusManager.EXPECT().
		GetWorkload(gomock.Any(), "test-workload").
		Return(core.Workload{}, errors.New("workload not found")).
		AnyTimes()

	// Expect SetWorkloadStatus to be called when token becomes invalid
	statusManager.EXPECT().
		SetWorkloadStatus(gomock.Any(), "test-workload", rt.WorkloadStatusUnauthenticated, gomock.Any()).
		Return(nil).
		Times(1)

	// Create authenticated token source (starts background monitoring)
	ats := NewAuthenticatedTokenSource(tokenSource, statusManager, "test-workload")
	defer ats.StopMonitoring()

	// Wait a bit longer than the expiry to let the background checker run
	time.Sleep(2 * time.Second)
}
