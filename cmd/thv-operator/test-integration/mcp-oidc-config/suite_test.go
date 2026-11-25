// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the MCPOIDCConfig controller
package controllers

import (
	"context"
	"sync"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "MCPOIDCConfig Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartSuite(testutil.SuiteOptions{
		RegisterGroupRefIndexers: true,
	})
	cfg = suiteEnv.Cfg
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx

	// Register the MCPOIDCConfig controller. Its client records reads of
	// deletion-blocked configs so specs can wait for the reconcile queued by the
	// DeletionBlocked status patch to drain.
	err := (&controllers.MCPOIDCConfigReconciler{
		Client: &blockedReadRecordingClient{Client: suiteEnv.Manager.GetClient()},
		Scheme: suiteEnv.Manager.GetScheme(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPServer controller (needed because MCPOIDCConfig watches
	// MCPServer changes and we test cross-resource interactions)
	err = (&controllers.MCPServerReconciler{
		Client:           suiteEnv.Manager.GetClient(),
		Scheme:           suiteEnv.Manager.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPGroup controller (VirtualMCPServer depends on MCPGroup)
	err = (&controllers.MCPGroupReconciler{
		Client: suiteEnv.Manager.GetClient(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the VirtualMCPServer controller (needed because MCPOIDCConfig watches
	// VirtualMCPServer changes and we test cross-resource interactions)
	err = (&controllers.VirtualMCPServerReconciler{
		Client:           suiteEnv.Manager.GetClient(),
		Scheme:           suiteEnv.Manager.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}).SetupWithManager(suiteEnv.Manager)
	Expect(err).ToNot(HaveOccurred())

	// Register the MCPRemoteProxy controller (needed because MCPOIDCConfig watches
	// MCPRemoteProxy changes and we test cross-resource interactions)
	err = (&controllers.MCPRemoteProxyReconciler{
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

// blockedReadKeys holds the MCPOIDCConfigs that a reconcile has read while they
// were already Terminating with DeletionBlocked=True.
var blockedReadKeys sync.Map

// blockedReadRecordingClient wraps the MCPOIDCConfig reconciler's client and
// records every Get that returns a config already in the blocked state. Such a
// read comes from a reconcile that started after the DeletionBlocked status
// patch reached the cache, not from the reconcile that wrote it.
type blockedReadRecordingClient struct {
	client.Client
}

func (c *blockedReadRecordingClient) Get(
	ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if cfg, ok := obj.(*mcpv1beta1.MCPOIDCConfig); ok && !cfg.DeletionTimestamp.IsZero() &&
		meta.IsStatusConditionTrue(cfg.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked) {
		blockedReadKeys.Store(key, struct{}{})
	}
	return nil
}
