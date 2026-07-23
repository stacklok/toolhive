// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the MCPServer untrusted mode:
// CEL admission rules R1-R6 and sentinel env injection in the backend pod patch.
package controllers

import (
	"context"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

var (
	untrustedCfg       *rest.Config
	untrustedK8sClient client.Client
	untrustedCtx       context.Context
	untrustedSuiteEnv  *testutil.SuiteEnv
)

func TestMCPServerUntrusted(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "MCPServer Untrusted Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	// Untrusted mode is opt-in (TOOLHIVE_ENABLE_UNTRUSTED_MODE, default off);
	// this suite exercises untrusted behavior end to end, so the operator
	// process under test must run with the mode enabled. Ginkgo keeps the
	// entire suite in one process, so setting the env var here (before the
	// manager starts) is equivalent to the chart injecting it on the
	// operator Deployment.
	Expect(os.Setenv("TOOLHIVE_ENABLE_UNTRUSTED_MODE", "true")).To(Succeed())

	untrustedSuiteEnv = testutil.StartSuite(testutil.SuiteOptions{
		RegisterGroupRefIndexers: true,
	})
	untrustedCfg = untrustedSuiteEnv.Cfg
	untrustedK8sClient = untrustedSuiteEnv.Client
	untrustedCtx = untrustedSuiteEnv.Ctx

	// Register the MCPGroup controller (needed for groupRef validation)
	err := (&controllers.MCPGroupReconciler{
		Client: untrustedSuiteEnv.Manager.GetClient(),
	}).SetupWithManager(untrustedSuiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPServer controller
	err = (&controllers.MCPServerReconciler{
		Client:           untrustedSuiteEnv.Manager.GetClient(),
		Scheme:           untrustedSuiteEnv.Manager.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
		APIReader:        untrustedSuiteEnv.Manager.GetAPIReader(),
	}).SetupWithManager(untrustedSuiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	untrustedSuiteEnv.StartManager()
})

var _ = AfterSuite(func() {
	untrustedSuiteEnv.Stop()
})
