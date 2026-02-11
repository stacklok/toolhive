// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package webhooks contains integration tests for admission webhooks
package webhooks

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestWebhooks(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Only show verbose output for failures
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "Webhook Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	// Only log errors unless a test fails
	logLevel := zapcore.ErrorLevel

	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true), zap.Level(logLevel)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping test environment with webhook support")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "..", "deploy", "charts", "operator-crds", "files", "crds")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			Paths: []string{filepath.Join("..", "..", "..", "..", "config", "webhook")},
		},
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = mcpv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// Add other schemes
	err = appsv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = corev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = rbacv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Start the controller manager with webhook server
	webhookInstallOptions := &testEnv.WebhookInstallOptions
	k8sManager, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0", // Disable metrics server for tests
		},
		WebhookServer: webhook.NewServer(webhook.Options{
			Host:    webhookInstallOptions.LocalServingHost,
			Port:    webhookInstallOptions.LocalServingPort,
			CertDir: webhookInstallOptions.LocalServingCertDir,
		}),
	})
	Expect(err).ToNot(HaveOccurred())

	// Set up webhooks
	err = (&mcpv1alpha1.VirtualMCPServer{}).SetupWebhookWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&mcpv1alpha1.VirtualMCPCompositeToolDefinition{}).SetupWebhookWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	err = (&mcpv1alpha1.MCPExternalAuthConfig{}).SetupWebhookWithManager(k8sManager)
	Expect(err).ToNot(HaveOccurred())

	// Start the manager
	go func() {
		defer GinkgoRecover()
		err = k8sManager.Start(ctx)
		Expect(err).ToNot(HaveOccurred(), "failed to run manager")
	}()

	// Wait for webhook server to be ready
	time.Sleep(1 * time.Second)
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
