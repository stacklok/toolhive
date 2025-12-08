package status

import (
	"context"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
)

// LogReporter implements logging status to stdout for
// CLI/local environments where Kubernetes is not available
type LogReporter struct {
	name      string
	stopCh    chan struct{}
	doneCh    chan struct{}
	getStatus func() *RuntimeStatus // Callback to get server status
}

// NewLogReporter creates a new LogReporter instance
func NewLogReporter(name string) *LogReporter {
	return &LogReporter{
		name:   name,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// SetStatusCallback sets the function to retrieve current server status
func (l *LogReporter) SetStatusCallback(fn func() *RuntimeStatus) {
	l.getStatus = fn
}

// Report logs status once to stdout
func (l *LogReporter) Report(ctx context.Context, status *RuntimeStatus) error {
	logger.Infof("[%s] Status Report:", l.name)
	logger.Infof("  Phase: %s", status.Phase)
	logger.Infof("  Message: %s", status.Message)
	logger.Infof("  Total Tools: %d", status.TotalToolCount)
	logger.Infof("  Healthy Backends: %d/%d",
		status.HealthyBackends,
		status.HealthyBackends+status.UnhealthyBackends)

	if len(status.Backends) > 0 {
		logger.Infof("  Backend Details:")
		for _, backend := range status.Backends {
			logger.Infof("    - %s: healthy=%v, msg=%s",
				backend.Name, backend.Healthy, backend.Message)
		}
	}

	return nil
}

// Start begins periodic status reporting in a background goroutine
func (l *LogReporter) Start(ctx context.Context, interval time.Duration) error {
	logger.Infof("[%s] Starting status reporter (interval: %v)", l.name, interval)
	go l.reportLoop(ctx, interval)
	return nil
}

// Stop gracefully stops the periodic reporter
func (l *LogReporter) Stop() {
	select {
	case <-l.stopCh:
		// Already stopped
		return
	default:
		close(l.stopCh)
		<-l.doneCh
	}
}

// reportLoop runs in a background goroutine and reports status periodically
func (l *LogReporter) reportLoop(ctx context.Context, interval time.Duration) {
	defer close(l.doneCh)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			logger.Debugf("[%s] Periodic report tick", l.name)

			// Get status from server if callback is set
			var status *RuntimeStatus
			if l.getStatus != nil {
				status = l.getStatus()
			} else {
				// Fallback: create basic status
				status = &RuntimeStatus{
					Phase:             PhaseReady,
					Message:           "No status callback configured",
					TotalToolCount:    0,
					HealthyBackends:   0,
					UnhealthyBackends: 0,
					Backends:          []BackendHealthReport{},
					LastDiscoveryTime: time.Now(),
				}
			}

			// Report the status
			if err := l.Report(ctx, status); err != nil {
				logger.Errorf("[%s] Failed to report status: %v", l.name, err)
			}

		case <-l.stopCh:
			logger.Infof("[%s] Status reporter stopping", l.name)
			return

		case <-ctx.Done():
			logger.Infof("[%s] Status reporter context cancelled", l.name)
			return
		}
	}
}
