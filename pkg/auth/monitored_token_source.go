package auth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"golang.org/x/oauth2"
)

// StatusUpdater is an interface for updating workload authentication status.
// This abstraction allows the monitored token source to work with any status management system
// without creating import cycles.
type StatusUpdater interface {
	SetWorkloadStatus(ctx context.Context, workloadName string, status runtime.WorkloadStatus, reason string) error
}

// MonitoredTokenSource is a wrapper around an oauth2.TokenSource that monitors authentication
// failures and automatically marks workloads as unauthenticated when tokens expire or fail.
// It provides both per-request token retrieval and background monitoring.
type MonitoredTokenSource struct {
	tokenSource    oauth2.TokenSource
	workloadName   string
	statusUpdater  StatusUpdater
	monitoringCtx  context.Context
	stopMonitoring chan struct{}
	stopOnce       sync.Once

	timer *time.Timer
}

// NewMonitoredTokenSource creates a new MonitoredTokenSource that wraps the provided
// oauth2.TokenSource and monitors it for authentication failures.
func NewMonitoredTokenSource(
	ctx context.Context,
	tokenSource oauth2.TokenSource,
	workloadName string,
	statusUpdater StatusUpdater,
) *MonitoredTokenSource {
	return &MonitoredTokenSource{
		tokenSource:    tokenSource,
		workloadName:   workloadName,
		statusUpdater:  statusUpdater,
		monitoringCtx:  ctx,
		stopMonitoring: make(chan struct{}),
	}
}

// Token retrieves a token from the token source and will mark the workload as unauthenticated
// if the token retrieval fails.
func (mts *MonitoredTokenSource) Token() (*oauth2.Token, error) {
	tok, err := mts.tokenSource.Token()
	if err != nil {
		mts.markAsUnauthenticated(fmt.Sprintf("Token retrieval failed: %v", err))
		return nil, err
	}
	return tok, nil
}

// StartBackgroundMonitoring starts the background monitoring goroutine that checks
// token validity at expiry time and marks the workload as unauthenticated on the failure.
func (mts *MonitoredTokenSource) StartBackgroundMonitoring() {
	if mts.timer == nil {
		mts.timer = time.NewTimer(time.Millisecond) // kick immediately
	}
	go mts.monitorLoop()
}

func (mts *MonitoredTokenSource) monitorLoop() {
	for {
		select {
		case <-mts.monitoringCtx.Done():
			mts.stopTimer()
			return
		case <-mts.stopMonitoring:
			mts.stopTimer()
			return
		case <-mts.timer.C:
			shouldStop, next := mts.onTick()
			if shouldStop {
				mts.stopTimer()
				return
			}
			mts.resetTimer(next)
		}
	}
}

func (mts *MonitoredTokenSource) stopTimer() {
	if mts.timer != nil && !mts.timer.Stop() {
		select {
		case <-mts.timer.C:
		default:
		}
	}
}

func (mts *MonitoredTokenSource) resetTimer(d time.Duration) {
	mts.stopTimer()
	mts.timer.Reset(d)
}

// onTick returns (shouldStop bool, nextDelay time.Duration)
func (mts *MonitoredTokenSource) onTick() (bool, time.Duration) {
	tok, err := mts.tokenSource.Token()
	if err != nil {
		// Any error → mark as unauthenticated and stop
		mts.markAsUnauthenticated(fmt.Sprintf("No valid token: %v", err))
		return true, 0
	}

	// Success → schedule next check
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

// markAsUnauthenticated marks the workload as unauthenticated.
func (mts *MonitoredTokenSource) markAsUnauthenticated(reason string) {
	_ = mts.statusUpdater.SetWorkloadStatus(
		context.Background(),
		mts.workloadName,
		runtime.WorkloadStatusUnauthenticated,
		reason,
	)
	mts.stopOnce.Do(func() { close(mts.stopMonitoring) })
}
