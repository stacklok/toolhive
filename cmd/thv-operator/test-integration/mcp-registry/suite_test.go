// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package operator_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

var (
	k8sClient client.Client
	ctx       context.Context
	suiteEnv  *testutil.SuiteEnv
)

func TestOperatorE2E(t *testing.T) { //nolint:paralleltest // E2E tests should not run in parallel
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Only show verbose output for failures
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "MCPRegistry Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{})
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Verify MCPRegistry CRD is available
	By("verifying MCPRegistry CRD is available")
	Eventually(func() error {
		mcpRegistry := &mcpv1beta1.MCPRegistry{}
		return k8sClient.Get(ctx, client.ObjectKey{
			Namespace: "default",
			Name:      "test-availability-check",
		}, mcpRegistry)
	}, time.Minute, time.Second).Should(MatchError(ContainSubstring("not found")))

	// Set up MCPRegistry controller
	By("setting up MCPRegistry controller")
	err := controllers.NewMCPRegistryReconciler(
		suiteEnv.Manager.GetClient(), suiteEnv.Manager.GetScheme(),
		suiteEnv.Manager.GetEventRecorder("mcpregistry-controller"), imagepullsecrets.Defaults{},
	).SetupWithManager(suiteEnv.Manager)
	Expect(err).NotTo(HaveOccurred())

	// Start the manager in the background
	By("starting controller manager")
	suiteEnv.StartManager()

	// Wait for the manager to be ready
	By("waiting for controller manager to be ready")
	Eventually(func() bool {
		return suiteEnv.Manager.GetCache().WaitForCacheSync(ctx)
	}, time.Minute, time.Second).Should(BeTrue())
})

var _ = AfterSuite(func() {
	suiteEnv.Stop()
})
