// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the MCPExternalAuthConfig controller
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

	RunSpecs(t, "MCPExternalAuthConfig Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{})
	cfg = suiteEnv.Cfg
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Register the MCPExternalAuthConfig controller
	err := (&controllers.MCPExternalAuthConfigReconciler{
		Client: suiteEnv.Manager.GetClient(),
		Scheme: suiteEnv.Manager.GetScheme(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPServer controller (needed for testing integration)
	err = (&controllers.MCPServerReconciler{
		Client:           suiteEnv.Manager.GetClient(),
		Scheme:           suiteEnv.Manager.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
		APIReader:        suiteEnv.Manager.GetAPIReader(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	suiteEnv.StartManager()
})

var _ = AfterSuite(func() {
	suiteEnv.Stop()
})
