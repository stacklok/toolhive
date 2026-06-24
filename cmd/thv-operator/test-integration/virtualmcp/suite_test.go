// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the VirtualMCPServer controller
package controllers

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	suiteEnv  *testutil.SuiteEnv
)

func TestControllers(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Only show verbose output for failures
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "VirtualMCPServer Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{
		RegisterGroupRefIndexers: true,
	})
	cfg = suiteEnv.Cfg
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Register the MCPGroup controller (required by VirtualMCPServer)
	err := (&controllers.MCPGroupReconciler{
		Client: suiteEnv.Manager.GetClient(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPTelemetryConfig controller (required for telemetryConfigRef tests)
	err = (&controllers.MCPTelemetryConfigReconciler{
		Client: suiteEnv.Manager.GetClient(),
		Scheme: suiteEnv.Manager.GetScheme(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the VirtualMCPServer controller
	err = (&controllers.VirtualMCPServerReconciler{
		Client:           suiteEnv.Manager.GetClient(),
		Scheme:           suiteEnv.Manager.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	suiteEnv.StartManager()
})

var _ = AfterSuite(func() {
	suiteEnv.Stop()
})
