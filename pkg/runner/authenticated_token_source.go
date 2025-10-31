package runner

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
)

// AuthenticatedTokenSource wraps an OAuth2 TokenSource and monitors authentication failures.
// It marks workloads as unauthenticated when tokens are expired and cannot be refreshed.
type AuthenticatedTokenSource struct {
	// tokenSource is the underlying OAuth2 token source
	tokenSource oauth2.TokenSource

	// statusManager is used to update workload status
	statusManager statuses.StatusManager

	// workloadName is the name of the workload to update
	workloadName string

	// Background monitoring
	stopMonitoring   chan struct{}
	monitoringCtx    context.Context
	monitoringCancel context.CancelFunc
}

// NewAuthenticatedTokenSource creates a new authenticated token source wrapper
func NewAuthenticatedTokenSource(
	tokenSource oauth2.TokenSource,
	statusManager statuses.StatusManager,
	workloadName string,
) *AuthenticatedTokenSource {
	ctx, cancel := context.WithCancel(context.Background())

	ats := &AuthenticatedTokenSource{
		tokenSource:      tokenSource,
		statusManager:    statusManager,
		workloadName:     workloadName,
		stopMonitoring:   make(chan struct{}),
		monitoringCtx:    ctx,
		monitoringCancel: cancel,
	}

	// Start background monitoring
	go ats.startBackgroundMonitoring()

	return ats
}

// Token retrieves a token from the underlying token source
// If token retrieval fails, it immediately marks the workload as unauthenticated
func (ats *AuthenticatedTokenSource) Token() (*oauth2.Token, error) {
	token, err := ats.tokenSource.Token()
	if err != nil {
		// Token retrieval failed - mark as unauthenticated immediately
		// Check if not already unauthenticated to avoid unnecessary writes
		wl, getErr := ats.statusManager.GetWorkload(context.Background(), ats.workloadName)
		if getErr != nil || wl.Status != rt.WorkloadStatusUnauthenticated {
			ats.markAsUnauthenticated(fmt.Sprintf("Token retrieval failed: %v", err))
		}
		return nil, err
	}
	return token, nil
}

// startBackgroundMonitoring starts the background monitoring goroutine
func (ats *AuthenticatedTokenSource) startBackgroundMonitoring() {
	// Start with an immediate check to get the initial token
	ats.checkTokenValidity()
}

// scheduleNextCheck schedules the next token check using time.AfterFunc
func (ats *AuthenticatedTokenSource) scheduleNextCheck(token *oauth2.Token) {
	now := time.Now()
	expiry := token.Expiry

	// If expiry is zero, do not schedule
	if expiry.IsZero() {
		return
	}

	// Schedule check for when token expires
	waitTime := expiry.Sub(now)
	if waitTime <= 0 {
		waitTime = time.Second
	}
	time.AfterFunc(waitTime, func() {
		// Check if monitoring is still active
		select {
		case <-ats.monitoringCtx.Done():
			return
		case <-ats.stopMonitoring:
			return
		default:
			ats.checkTokenValidity()
		}
	})
}

// checkTokenValidity checks if we have a valid token, marks as unauthenticated if not
// and schedules the next check when the token expires.
func (ats *AuthenticatedTokenSource) checkTokenValidity() {
	// Let OAuth2 library handle token validation and refresh automatically
	token, err := ats.tokenSource.Token()
	if err != nil {
		// No valid token available. If not already unauthenticated, mark it.
		workload, getErr := ats.statusManager.GetWorkload(context.Background(), ats.workloadName)
		if getErr != nil || workload.Status != rt.WorkloadStatusUnauthenticated {
			ats.markAsUnauthenticated(fmt.Sprintf("No valid token: %v", err))
		}
		return
	}

	// If the token we obtained is already expired, treat as unauthenticated
	if token.Expiry.IsZero() || !token.Expiry.After(time.Now()) {
		ats.markAsUnauthenticated("Token already expired")
		return
	}

	// Schedule next check when this token expires
	ats.scheduleNextCheck(token)
}

// markAsUnauthenticated marks the workload as unauthenticated
func (ats *AuthenticatedTokenSource) markAsUnauthenticated(reason string) {

	// Set workload status to unauthenticated
	if setErr := ats.statusManager.SetWorkloadStatus(
		context.Background(),
		ats.workloadName,
		rt.WorkloadStatusUnauthenticated,
		reason,
	); setErr != nil {
		logger.Errorf("Failed to set workload %s status to unauthenticated: %v", ats.workloadName, setErr)
	}
}

// StopMonitoring stops the background monitoring.
// This method is idempotent and safe to call multiple times.
func (ats *AuthenticatedTokenSource) StopMonitoring() {
	// Cancel the context first
	ats.monitoringCancel()

	// Safely close the channel only if it hasn't been closed already
	select {
	case <-ats.stopMonitoring:
		// Channel already closed, nothing to do
	default:
		close(ats.stopMonitoring)
	}
}
