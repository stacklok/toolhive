// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package health_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/health"
)

func TestCircuitBreakerIntegration(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)
	RunSpecs(t, "Circuit Breaker Integration Suite")
}

// flakyBackendClient simulates a backend that can fail intermittently.
// It implements vmcp.BackendClient interface for use with health.NewMonitor.
type flakyBackendClient struct {
	mu sync.Mutex

	// Failure simulation
	consecutiveFails int // Number of consecutive failures to return
	failCount        int // Current count of consecutive failures returned

	// Call tracking
	checkCount atomic.Int64 // Total number of health checks performed

	// Behavior control
	shouldFail    atomic.Bool   // Explicit control over failure state
	responseDelay time.Duration // Simulate slow responses
}

func newFlakyBackendClient() *flakyBackendClient {
	return &flakyBackendClient{
		responseDelay: 10 * time.Millisecond,
	}
}

// ListCapabilities implements vmcp.BackendClient for health check purposes.
func (f *flakyBackendClient) ListCapabilities(ctx context.Context, _ *vmcp.BackendTarget) (*vmcp.CapabilityList, error) {
	f.checkCount.Add(1)

	// Simulate response delay
	if f.responseDelay > 0 {
		select {
		case <-time.After(f.responseDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	// Check explicit failure control
	if f.shouldFail.Load() {
		return nil, fmt.Errorf("backend unavailable (explicit)")
	}

	// Check consecutive failure pattern
	if f.consecutiveFails > 0 && f.failCount < f.consecutiveFails {
		f.failCount++
		return nil, fmt.Errorf("backend unavailable (%d/%d)", f.failCount, f.consecutiveFails)
	}

	// Reset fail count after pattern completes
	if f.failCount >= f.consecutiveFails {
		f.failCount = 0
	}

	// Return successful response
	return &vmcp.CapabilityList{
		Tools:     []vmcp.Tool{},
		Resources: []vmcp.Resource{},
		Prompts:   []vmcp.Prompt{},
	}, nil
}

// CallTool implements vmcp.BackendClient (not used in health checks).
func (*flakyBackendClient) CallTool(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any, _ map[string]any) (*vmcp.ToolCallResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// ReadResource implements vmcp.BackendClient (not used in health checks).
func (*flakyBackendClient) ReadResource(_ context.Context, _ *vmcp.BackendTarget, _ string) (*vmcp.ResourceReadResult, error) {
	return nil, fmt.Errorf("not implemented")
}

// GetPrompt implements vmcp.BackendClient (not used in health checks).
func (*flakyBackendClient) GetPrompt(_ context.Context, _ *vmcp.BackendTarget, _ string, _ map[string]any) (*vmcp.PromptGetResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *flakyBackendClient) setConsecutiveFails(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consecutiveFails = count
	f.failCount = 0
}

func (f *flakyBackendClient) setExplicitFailure(shouldFail bool) {
	f.shouldFail.Store(shouldFail)
}

func (f *flakyBackendClient) getCheckCount() int64 {
	return f.checkCount.Load()
}

var _ = Describe("Circuit Breaker Integration Tests", func() {
	var (
		ctx     context.Context
		cancel  context.CancelFunc
		monitor *health.Monitor
		client  *flakyBackendClient
		backend vmcp.Backend

		checkInterval      = 500 * time.Millisecond
		failureThreshold   = 3
		cbTimeout          = 2 * time.Second
		unhealthyThreshold = 3
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		client = newFlakyBackendClient()

		backend = vmcp.Backend{
			ID:            "test-backend-1",
			Name:          "test-backend",
			BaseURL:       "http://localhost:8080",
			TransportType: "streamable-http",
		}

		config := health.MonitorConfig{
			CheckInterval:      checkInterval,
			UnhealthyThreshold: unhealthyThreshold,
			Timeout:            5 * time.Second,
			DegradedThreshold:  2 * time.Second,
			CircuitBreaker: &health.CircuitBreakerConfig{
				Enabled:          true,
				FailureThreshold: failureThreshold,
				Timeout:          cbTimeout,
			},
		}

		var err error
		monitor, err = health.NewMonitor(client, []vmcp.Backend{backend}, config)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if monitor != nil {
			monitor.Stop()
		}
		cancel()
	})

	Context("Circuit Opens on Consecutive Failures", func() {
		It("should open circuit after threshold failures", func() {
			By("Starting with healthy backend")
			monitor.Start(ctx)

			// Wait for initial healthy check
			Eventually(func() vmcp.BackendHealthStatus {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return vmcp.BackendUnknown
				}
				return state.Status
			}, 3*time.Second, 100*time.Millisecond).Should(Equal(vmcp.BackendHealthy))

			By("Simulating backend failures")
			client.setExplicitFailure(true)

			By("Waiting for circuit to open")
			// Should take approximately: failureThreshold * checkInterval = 3 * 500ms = ~1.5 seconds
			Eventually(func() health.CircuitState {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			By("Verifying circuit state and backend status")
			state, err := monitor.GetBackendState(backend.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.CircuitState).To(Equal(health.CircuitOpen))
			Expect(state.Status).To(Equal(vmcp.BackendUnhealthy))
			Expect(state.ConsecutiveFailures).To(BeNumerically(">=", failureThreshold))

			GinkgoWriter.Printf("✓ Circuit opened after %d consecutive failures\n", state.ConsecutiveFailures)
		})

		It("should stop health checks while circuit is open", func() {
			By("Starting monitor and waiting for healthy state")
			monitor.Start(ctx)
			Eventually(func() vmcp.BackendHealthStatus {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return vmcp.BackendUnknown
				}
				return state.Status
			}, 3*time.Second, 100*time.Millisecond).Should(Equal(vmcp.BackendHealthy))

			By("Causing circuit to open")
			client.setExplicitFailure(true)
			Eventually(func() health.CircuitState {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			By("Recording check count when circuit opens")
			checksWhenOpen := client.getCheckCount()
			GinkgoWriter.Printf("Health checks when circuit opened: %d\n", checksWhenOpen)

			By("Waiting and verifying no new checks occur")
			time.Sleep(1 * time.Second)
			checksAfterWait := client.getCheckCount()

			// Should be same or very close (at most 1 additional check due to timing)
			Expect(checksAfterWait).To(BeNumerically("<=", checksWhenOpen+1))
			GinkgoWriter.Printf("Health checks after 1s wait: %d (diff: %d)\n",
				checksAfterWait, checksAfterWait-checksWhenOpen)
		})
	})

	Context("Circuit Recovery After Timeout", func() {
		It("should recover after timeout when backend is fixed", func() {
			By("Starting monitor and establishing healthy state")
			monitor.Start(ctx)
			Eventually(func() vmcp.BackendHealthStatus {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return vmcp.BackendUnknown
				}
				return state.Status
			}, 3*time.Second, 100*time.Millisecond).Should(Equal(vmcp.BackendHealthy))

			By("Opening circuit with failures")
			client.setExplicitFailure(true)
			Eventually(func() health.CircuitState {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			openTime := time.Now()
			GinkgoWriter.Printf("Circuit opened at: %s\n", openTime.Format(time.RFC3339))

			By("Fixing backend while circuit is open")
			client.setExplicitFailure(false)

			By("Waiting for circuit to recover (transitions through half-open and closes)")
			// After cbTimeout (2s), circuit attempts recovery.
			// If backend is healthy, it will transition half-open→closed quickly.
			Eventually(func() bool {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return false
				}
				// Circuit should eventually be closed and backend healthy
				return state.CircuitState == health.CircuitClosed && state.Status == vmcp.BackendHealthy
			}, cbTimeout+3*time.Second, 100*time.Millisecond).Should(BeTrue())

			elapsed := time.Since(openTime)
			GinkgoWriter.Printf("✓ Circuit recovered after: %s (timeout was %s)\n", elapsed, cbTimeout)

			// Recovery should take at least cbTimeout
			Expect(elapsed).To(BeNumerically(">=", cbTimeout))
		})
	})

	Context("Circuit Recovery Behavior", func() {
		It("should fully recover when backend is fixed", func() {
			By("Starting monitor and establishing healthy state")
			monitor.Start(ctx)
			Eventually(func() vmcp.BackendHealthStatus {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return vmcp.BackendUnknown
				}
				return state.Status
			}, 3*time.Second, 100*time.Millisecond).Should(Equal(vmcp.BackendHealthy))

			By("Opening circuit")
			client.setExplicitFailure(true)
			Eventually(func() health.CircuitState {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			By("Fixing backend")
			client.setExplicitFailure(false)

			By("Waiting for full recovery")
			// After timeout, circuit tests recovery and should fully close
			Eventually(func() bool {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return false
				}
				return state.CircuitState == health.CircuitClosed &&
					state.Status == vmcp.BackendHealthy &&
					state.ConsecutiveFailures == 0
			}, cbTimeout+3*time.Second, 100*time.Millisecond).Should(BeTrue())

			GinkgoWriter.Println("✓ Circuit successfully recovered and closed")
		})

		It("should remain open if backend stays broken", func() {
			By("Starting monitor and establishing healthy state")
			monitor.Start(ctx)
			Eventually(func() vmcp.BackendHealthStatus {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return vmcp.BackendUnknown
				}
				return state.Status
			}, 3*time.Second, 100*time.Millisecond).Should(Equal(vmcp.BackendHealthy))

			By("Opening circuit with failures")
			client.setExplicitFailure(true)
			Eventually(func() health.CircuitState {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			By("Waiting past timeout period (backend still broken)")
			time.Sleep(cbTimeout + 1*time.Second)

			By("Verifying circuit remains open after failed recovery attempt")
			// Circuit will attempt recovery, fail, and reopen
			Eventually(func() health.CircuitState {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 2*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			state, err := monitor.GetBackendState(backend.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.Status).To(Equal(vmcp.BackendUnhealthy))

			GinkgoWriter.Println("✓ Circuit reopened after failed recovery attempt")
		})
	})

	Context("Intermittent Failures", func() {
		It("should not open circuit if failures are below threshold", func() {
			By("Starting monitor with pattern of 2 failures then success")
			client.setConsecutiveFails(2) // Less than threshold (3)
			monitor.Start(ctx)

			By("Waiting for multiple check cycles")
			time.Sleep(3 * time.Second)

			By("Verifying circuit remains closed")
			state, err := monitor.GetBackendState(backend.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.CircuitState).To(Equal(health.CircuitClosed))

			// Status should be healthy or degraded, but not unavailable
			Expect(state.Status).To(Or(
				Equal(vmcp.BackendHealthy),
				Equal(vmcp.BackendDegraded),
			))

			GinkgoWriter.Printf("✓ Circuit remained closed with intermittent failures (consecutive: %d)\n",
				state.ConsecutiveFailures)
		})

		It("should reset failure count after successful check", func() {
			By("Starting monitor and establishing healthy state")
			monitor.Start(ctx)
			Eventually(func() vmcp.BackendHealthStatus {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return vmcp.BackendUnknown
				}
				return state.Status
			}, 3*time.Second, 100*time.Millisecond).Should(Equal(vmcp.BackendHealthy))

			By("Causing 2 failures (below threshold)")
			client.setConsecutiveFails(2)

			Eventually(func() int {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return -1
				}
				return state.ConsecutiveFailures
			}, 3*time.Second, 100*time.Millisecond).Should(BeNumerically(">=", 2))

			By("Allowing recovery (next check succeeds after pattern completes)")
			Eventually(func() int {
				state, err := monitor.GetBackendState(backend.ID)
				if err != nil {
					return -1
				}
				return state.ConsecutiveFailures
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(0))

			By("Verifying backend is healthy or degraded (recovering)")
			state, err := monitor.GetBackendState(backend.ID)
			Expect(err).ToNot(HaveOccurred())
			// After recovery, backend might be degraded before fully stabilizing
			Expect(state.Status).To(Or(
				Equal(vmcp.BackendHealthy),
				Equal(vmcp.BackendDegraded),
			))

			GinkgoWriter.Printf("✓ Failure count reset after successful check (status: %s)\n", state.Status)
		})
	})

	Context("Configuration", func() {
		It("should respect custom failure threshold", func() {
			By("Creating monitor with failure threshold of 5")
			customConfig := health.MonitorConfig{
				CheckInterval:      checkInterval,
				UnhealthyThreshold: unhealthyThreshold,
				Timeout:            5 * time.Second,
				DegradedThreshold:  2 * time.Second,
				CircuitBreaker: &health.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: 5, // Custom threshold
					Timeout:          cbTimeout,
				},
			}

			customMonitor, err := health.NewMonitor(client, []vmcp.Backend{backend}, customConfig)
			Expect(err).ToNot(HaveOccurred())
			defer customMonitor.Stop()

			By("Starting monitor and causing failures")
			client.setExplicitFailure(true)
			customMonitor.Start(ctx)

			By("Verifying circuit doesn't open after 3 failures")
			time.Sleep(4 * checkInterval)
			state, err := customMonitor.GetBackendState(backend.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.CircuitState).To(Equal(health.CircuitClosed))

			By("Waiting for 5+ failures")
			Eventually(func() health.CircuitState {
				state, err := customMonitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 6*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			GinkgoWriter.Println("✓ Custom failure threshold respected")
		})

		It("should respect custom timeout duration", func() {
			By("Creating monitor with short timeout")
			shortTimeout := 1 * time.Second
			customConfig := health.MonitorConfig{
				CheckInterval:      checkInterval,
				UnhealthyThreshold: unhealthyThreshold,
				Timeout:            5 * time.Second,
				DegradedThreshold:  2 * time.Second,
				CircuitBreaker: &health.CircuitBreakerConfig{
					Enabled:          true,
					FailureThreshold: failureThreshold,
					Timeout:          shortTimeout,
				},
			}

			customMonitor, err := health.NewMonitor(client, []vmcp.Backend{backend}, customConfig)
			Expect(err).ToNot(HaveOccurred())
			defer customMonitor.Stop()

			By("Opening circuit")
			client.setExplicitFailure(true)
			customMonitor.Start(ctx)

			Eventually(func() health.CircuitState {
				state, err := customMonitor.GetBackendState(backend.ID)
				if err != nil {
					return ""
				}
				return state.CircuitState
			}, 5*time.Second, 100*time.Millisecond).Should(Equal(health.CircuitOpen))

			openTime := time.Now()

			By("Fixing backend and waiting for recovery")
			client.setExplicitFailure(false)

			// After shortTimeout, circuit should attempt recovery and succeed
			Eventually(func() bool {
				state, err := customMonitor.GetBackendState(backend.ID)
				if err != nil {
					return false
				}
				return state.CircuitState == health.CircuitClosed && state.Status == vmcp.BackendHealthy
			}, shortTimeout+2*time.Second, 100*time.Millisecond).Should(BeTrue())

			elapsed := time.Since(openTime)
			GinkgoWriter.Printf("✓ Custom timeout respected: recovered after %s (timeout was %s)\n",
				elapsed, shortTimeout)

			// Should take at least the timeout duration
			Expect(elapsed).To(BeNumerically(">=", shortTimeout))
		})
	})

	Context("Circuit Breaker Disabled", func() {
		It("should not open circuit when disabled", func() {
			By("Creating monitor with circuit breaker disabled")
			config := health.MonitorConfig{
				CheckInterval:      checkInterval,
				UnhealthyThreshold: unhealthyThreshold,
				Timeout:            5 * time.Second,
				DegradedThreshold:  2 * time.Second,
				CircuitBreaker:     nil, // Disabled
			}

			disabledMonitor, err := health.NewMonitor(client, []vmcp.Backend{backend}, config)
			Expect(err).ToNot(HaveOccurred())
			defer disabledMonitor.Stop()

			By("Causing many failures")
			client.setExplicitFailure(true)
			disabledMonitor.Start(ctx)

			By("Waiting for multiple check cycles")
			time.Sleep(3 * time.Second)

			By("Verifying status becomes unhealthy but circuit doesn't open")
			state, err := disabledMonitor.GetBackendState(backend.ID)
			Expect(err).ToNot(HaveOccurred())
			Expect(state.Status).To(Equal(vmcp.BackendUnhealthy))
			// Circuit state should be empty/uninitialized when disabled
			Expect(state.CircuitState).To(Equal(health.CircuitState("")))

			GinkgoWriter.Println("✓ Circuit breaker disabled, status tracking still works")
		})
	})
})
