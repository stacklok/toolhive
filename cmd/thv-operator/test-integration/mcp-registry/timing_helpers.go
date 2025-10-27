package operator_test

import (
	"context"
	"time"

	"github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// TimingTestHelper provides utilities for timing and synchronization in async operations
type TimingTestHelper struct {
	Client  client.Client
	Context context.Context
}

// NewTimingTestHelper creates a new test helper for timing operations
func NewTimingTestHelper(ctx context.Context, k8sClient client.Client) *TimingTestHelper {
	return &TimingTestHelper{
		Client:  k8sClient,
		Context: ctx,
	}
}

// Common timeout values for different types of operations
const (
	// QuickTimeout for operations that should complete quickly (e.g., resource creation)
	QuickTimeout = 10 * time.Second

	// MediumTimeout for operations that may take some time (e.g., controller reconciliation)
	MediumTimeout = 30 * time.Second

	// LongTimeout for operations that may take a while (e.g., sync operations)
	LongTimeout = 2 * time.Minute

	// ExtraLongTimeout for operations that may take very long (e.g., complex e2e scenarios)
	ExtraLongTimeout = 5 * time.Minute

	// DefaultPollingInterval for Eventually/Consistently checks
	DefaultPollingInterval = 1 * time.Second

	// FastPollingInterval for operations that need frequent checks
	FastPollingInterval = 200 * time.Millisecond

	// SlowPollingInterval for operations that don't need frequent checks
	SlowPollingInterval = 5 * time.Second
)

// EventuallyWithTimeout runs an Eventually check with custom timeout and polling
func (*TimingTestHelper) EventuallyWithTimeout(assertion func() interface{},
	timeout, polling time.Duration) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, timeout, polling)
}

// ConsistentlyWithTimeout runs a Consistently check with custom timeout and polling
func (*TimingTestHelper) ConsistentlyWithTimeout(assertion func() interface{},
	duration, polling time.Duration) gomega.AsyncAssertion {
	return gomega.Consistently(assertion, duration, polling)
}

// WaitForResourceCreation waits for a resource to be created with quick timeout
func (*TimingTestHelper) WaitForResourceCreation(assertion func() interface{}) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, QuickTimeout, FastPollingInterval)
}

// WaitForControllerReconciliation waits for controller to reconcile changes
func (*TimingTestHelper) WaitForControllerReconciliation(assertion func() interface{}) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, MediumTimeout, DefaultPollingInterval)
}

// WaitForSyncOperation waits for a sync operation to complete
func (*TimingTestHelper) WaitForSyncOperation(assertion func() interface{}) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, LongTimeout, DefaultPollingInterval)
}

// WaitForComplexOperation waits for complex multi-step operations
func (*TimingTestHelper) WaitForComplexOperation(assertion func() interface{}) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, ExtraLongTimeout, SlowPollingInterval)
}

// EnsureStableState ensures a condition remains stable for a period
func (*TimingTestHelper) EnsureStableState(assertion func() interface{}, duration time.Duration) gomega.AsyncAssertion {
	return gomega.Consistently(assertion, duration, DefaultPollingInterval)
}

// EnsureQuickStability ensures a condition remains stable for a short period
func (h *TimingTestHelper) EnsureQuickStability(assertion func() interface{}) gomega.AsyncAssertion {
	return h.EnsureStableState(assertion, 5*time.Second)
}

// TimeoutConfig represents timeout configuration for different scenarios
type TimeoutConfig struct {
	Timeout         time.Duration
	PollingInterval time.Duration
	Description     string
}

// GetTimeoutForOperation returns appropriate timeout configuration for different operation types
func (*TimingTestHelper) GetTimeoutForOperation(operationType string) TimeoutConfig {
	switch operationType {
	case "create":
		return TimeoutConfig{
			Timeout:         QuickTimeout,
			PollingInterval: FastPollingInterval,
			Description:     "Resource creation",
		}
	case "reconcile":
		return TimeoutConfig{
			Timeout:         MediumTimeout,
			PollingInterval: DefaultPollingInterval,
			Description:     "Controller reconciliation",
		}
	case "sync":
		return TimeoutConfig{
			Timeout:         LongTimeout,
			PollingInterval: DefaultPollingInterval,
			Description:     "Sync operation",
		}
	case "complex":
		return TimeoutConfig{
			Timeout:         ExtraLongTimeout,
			PollingInterval: SlowPollingInterval,
			Description:     "Complex operation",
		}
	case "delete":
		return TimeoutConfig{
			Timeout:         MediumTimeout,
			PollingInterval: DefaultPollingInterval,
			Description:     "Resource deletion",
		}
	case "status-update":
		return TimeoutConfig{
			Timeout:         MediumTimeout,
			PollingInterval: FastPollingInterval,
			Description:     "Status update",
		}
	default:
		return TimeoutConfig{
			Timeout:         MediumTimeout,
			PollingInterval: DefaultPollingInterval,
			Description:     "Default operation",
		}
	}
}

// WaitWithCustomTimeout waits with custom timeout configuration
func (*TimingTestHelper) WaitWithCustomTimeout(assertion func() interface{}, config TimeoutConfig) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, config.Timeout, config.PollingInterval)
}
