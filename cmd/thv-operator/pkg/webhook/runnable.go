// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package webhook

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var runnableLogger = log.Log.WithName("webhook-ca-injector")

// CABundleInjectorRunnable is a manager.Runnable that retries CA bundle injection
// This handles the case where the ValidatingWebhookConfiguration is created by Helm
// after the operator pod starts
type CABundleInjectorRunnable struct {
	Config SetupConfig
	Mgr    manager.Manager
}

// Start implements manager.Runnable
func (r *CABundleInjectorRunnable) Start(ctx context.Context) error {
	runnableLogger.Info("Starting CA bundle injector runnable")

	// Wait for the manager's cache to sync before starting
	if !r.Mgr.GetCache().WaitForCacheSync(ctx) {
		return fmt.Errorf("failed to wait for cache sync")
	}

	// Read the CA bundle from the certificate file
	certGen := NewCertGenerator(r.Config.ServiceName, r.Config.Namespace)
	caBundle, err := certGen.GetCABundle()
	if err != nil {
		return fmt.Errorf("failed to get CA bundle: %w", err)
	}

	// Retry CA bundle injection with exponential backoff
	maxRetries := 10
	initialDelay := 2 * time.Second
	maxDelay := 30 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		runnableLogger.Info("Attempting to inject CA bundle", "attempt", attempt, "maxRetries", maxRetries)

		err := InjectCABundle(ctx, r.Mgr.GetClient(), r.Config.WebhookConfigName, caBundle)
		if err == nil {
			runnableLogger.Info("Successfully injected CA bundle into webhook configuration",
				"webhookConfig", r.Config.WebhookConfigName,
			)
			// Success! We can stop retrying
			return nil
		}

		runnableLogger.Info("Failed to inject CA bundle, will retry",
			"webhookConfig", r.Config.WebhookConfigName,
			"error", err.Error(),
			"attempt", attempt,
			"maxRetries", maxRetries,
		)

		// Calculate delay with exponential backoff
		// Use min() to avoid overflow and cap at maxDelay
		backoffMultiplier := 1 << (attempt - 1)
		delay := min(time.Duration(backoffMultiplier)*initialDelay, maxDelay)

		if attempt < maxRetries {
			runnableLogger.V(1).Info("Waiting before retry", "delay", delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	// If we exhausted all retries, log a warning but don't fail
	// The operator should still function, just without webhook validation
	runnableLogger.Info("Exhausted all retries for CA bundle injection",
		"webhookConfig", r.Config.WebhookConfigName,
		"maxRetries", maxRetries,
	)

	// Don't return an error - let the operator continue running
	// The webhooks might work if manually configured
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable
// CA bundle injection should only run on the leader to avoid conflicts
func (*CABundleInjectorRunnable) NeedLeaderElection() bool {
	return true
}
