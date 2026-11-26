// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ownerrefadmission contains an integration test that runs the
// operator-generated proxy-runner RBAC against a kube-apiserver with the
// OwnerReferencesPermissionEnforcement admission plugin enabled.
package ownerrefadmission

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

func TestOwnerRefAdmission(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "MCPServer OwnerReferencesPermissionEnforcement Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	// This suite owns its apiserver flags so that no other suite's API server
	// changes. envtest already authorizes with RBAC, which the plugin consults.
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{
		EnableAdmissionPlugins: []string{"OwnerReferencesPermissionEnforcement"},
	})
	cfg = suiteEnv.Cfg
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Only the MCPServer controller is needed: it creates the proxy-runner
	// ServiceAccount, Role and RoleBinding under test.
	err := (&controllers.MCPServerReconciler{
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
