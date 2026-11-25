// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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

// WaitForControllerReconciliation waits for controller to reconcile changes
func (*TimingTestHelper) WaitForControllerReconciliation(assertion func() interface{}) gomega.AsyncAssertion {
	return gomega.Eventually(assertion, MediumTimeout, DefaultPollingInterval)
}
