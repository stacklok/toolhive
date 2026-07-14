// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the MCPTelemetryConfig controller
package controllers

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

var (
	k8sClient client.Client
	ctx       context.Context
	suiteEnv  *testutil.SuiteEnv
)

func TestControllers(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "MCPTelemetryConfig Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{})
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Register the MCPTelemetryConfig controller
	err := (&controllers.MCPTelemetryConfigReconciler{
		Client: suiteEnv.Manager.GetClient(),
		Scheme: suiteEnv.Manager.GetScheme(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	suiteEnv.StartManager()
})

var _ = AfterSuite(func() {
	suiteEnv.Stop()
})
