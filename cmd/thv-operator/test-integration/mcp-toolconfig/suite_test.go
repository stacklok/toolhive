// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package mcptoolconfig_test contains integration tests for the MCPToolConfig controller
package mcptoolconfig_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

var (
	k8sClient client.Client
	ctx       context.Context
	suiteEnv  *testutil.SuiteEnv
)

func TestMCPToolConfig(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Only show verbose output for failures
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "MCPToolConfig Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{})
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Register the MCPToolConfig controller (the controller under test)
	err := (&controllers.ToolConfigReconciler{
		Client: suiteEnv.Manager.GetClient(),
		Scheme: suiteEnv.Manager.GetScheme(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPServer controller (needed because ToolConfig watches
	// MCPServer changes and we test cross-resource interactions)
	err = (&controllers.MCPServerReconciler{
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
