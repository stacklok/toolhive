package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/workloads/statuses"
)

// AuthenticatedTokenSource is a wrapper around an oauth2.TokenSource that will mark the workload as unauthenticated
// if the token retrieval fails.
type AuthenticatedTokenSource struct {
	tokenSource    oauth2.TokenSource
	workloadName   string
	statusManager  statuses.StatusManager
	monitoringCtx  context.Context
	stopMonitoring chan struct{}
	stopOnce       sync.Once

	timer      *time.Timer
	backoff    time.Duration
	maxBackoff time.Duration
}

// NewAuthenticatedTokenSource creates a new AuthenticatedTokenSource.
func NewAuthenticatedTokenSource(
	ctx context.Context,
	tokenSource oauth2.TokenSource,
	workloadName string,
	statusMgr statuses.StatusManager,
) *AuthenticatedTokenSource {
	return &AuthenticatedTokenSource{
		tokenSource:    tokenSource,
		workloadName:   workloadName,
		statusManager:  statusMgr,
		monitoringCtx:  ctx,
		stopMonitoring: make(chan struct{}),
	}
}

// Token retrieves a token from the token source and will mark the workload as unauthenticated
// if the token retrieval fails.
func (ats *AuthenticatedTokenSource) Token() (*oauth2.Token, error) {
	tok, err := ats.tokenSource.Token()
	if err != nil {
		if isAuthenticationError(err) {
			ats.markAsUnauthenticated(fmt.Sprintf("Token retrieval failed: %v", err))
		}
		return nil, err
	}
	return tok, nil
}

func (ats *AuthenticatedTokenSource) startBackgroundMonitoring() {
	if ats.timer == nil {
		ats.timer = time.NewTimer(time.Millisecond) // kick immediately
	}
	if ats.backoff == 0 {
		ats.backoff = time.Second
	}
	if ats.maxBackoff == 0 {
		ats.maxBackoff = 2 * time.Minute
	}
	go ats.monitorLoop()
}

func (ats *AuthenticatedTokenSource) monitorLoop() {
	for {
		select {
		case <-ats.monitoringCtx.Done():
			ats.stopTimer()
			return
		case <-ats.stopMonitoring:
			ats.stopTimer()
			return
		case <-ats.timer.C:
			shouldStop, next := ats.onTick()
			if shouldStop {
				ats.stopTimer()
				return
			}
			ats.resetTimer(next)
		}
	}
}
func (ats *AuthenticatedTokenSource) stopTimer() {
	if ats.timer != nil && !ats.timer.Stop() {
		select {
		case <-ats.timer.C:
		default:
		}
	}
}

func (ats *AuthenticatedTokenSource) resetTimer(d time.Duration) {
	ats.stopTimer()
	ats.timer.Reset(d)
}

// onTick returns (shouldStop bool, nextDelay time.Duration)
func (ats *AuthenticatedTokenSource) onTick() (bool, time.Duration) {
	tok, err := ats.tokenSource.Token()
	if err != nil {
		// Hard OAuth failure → flip & stop
		if isAuthenticationError(err) {
			ats.markAsUnauthenticated(fmt.Sprintf("No valid token: %v", err))
			return true, 0
		}
		// Transient → backoff and retry
		if ats.backoff == 0 {
			ats.backoff = time.Second
		}
		d := ats.backoff
		ats.backoff *= 2
		if ats.maxBackoff > 0 && (ats.backoff == 0 || ats.backoff > ats.maxBackoff) {
			ats.backoff = ats.maxBackoff
		}
		return false, d
	}

	// Success → schedule next check
	ats.backoff = time.Second
	if tok.Expiry.IsZero() {
		// no expiry → nothing to monitor
		return true, 0
	}
	wait := time.Until(tok.Expiry)
	if wait < time.Second {
		wait = time.Second
	}
	return false, wait
}

// markAsUnauthenticated consults StatusManager; only writes if not already unauthenticated.
// Always stops the monitor (idempotently).
func (ats *AuthenticatedTokenSource) markAsUnauthenticated(reason string) {
	_ = ats.statusManager.SetWorkloadStatus(
		context.Background(),
		ats.workloadName,
		rt.WorkloadStatusUnauthenticated,
		reason,
	)
	ats.stopOnce.Do(func() { close(ats.stopMonitoring) })
}

func isAuthenticationError(err error) bool {
	var r *oauth2.RetrieveError
	if errors.As(err, &r) {
		if r.Response != nil {
			switch r.Response.StatusCode {
			case http.StatusUnauthorized, http.StatusBadRequest:
				return true
			}
		}
		body := strings.ToLower(string(r.Body))
		if strings.Contains(body, "invalid_grant") ||
			strings.Contains(body, "invalid_client") ||
			strings.Contains(body, "invalid_token") {
			return true
		}
	}

	return false
}
