// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"os"
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
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg        *rest.Config
	k8sClient  client.Client
	ctx        context.Context
	cancel     context.CancelFunc
	kubeconfig string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	// Show verbose output for e2e tests
	reporterConfig.Verbose = true

	RunSpecs(t, "VirtualMCPServer E2E Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	logLevel := zapcore.InfoLevel
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true), zap.Level(logLevel)))

	ctx, cancel = context.WithCancel(context.Background())

	// Get kubeconfig path from environment or default
	kubeconfig = os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		Expect(err).NotTo(HaveOccurred())
		kubeconfig = homeDir + "/.kube/config"
	}

	By("loading kubeconfig from: " + kubeconfig)

	// Check if kubeconfig file exists
	_, err := os.Stat(kubeconfig)
	Expect(err).NotTo(HaveOccurred(), "kubeconfig file should exist at "+kubeconfig)

	// Build config from kubeconfig
	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	// Register schemes
	err = mcpv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = appsv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = corev1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	err = rbacv1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	// Create Kubernetes client
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	By("connected to Kubernetes cluster successfully")
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	time.Sleep(100 * time.Millisecond)
})
