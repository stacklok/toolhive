// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver contains e2e tests for authServerRef on MCPServer and MCPRemoteProxy resources.
package authserver

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

var (
	cfg        *rest.Config
	k8sClient  client.Client
	ctx        context.Context
	cancel     context.CancelFunc
	kubeconfig string
)

func TestE2E(t *testing.T) {
	t.Parallel()
	gomega.RegisterFailHandler(ginkgo.Fail)

	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	reporterConfig.Verbose = true

	ginkgo.RunSpecs(t, "AuthServerRef E2E Test Suite", suiteConfig, reporterConfig)
}

var _ = ginkgo.BeforeSuite(func() {
	logLevel := zapcore.InfoLevel
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true), zap.Level(logLevel)))

	ctx, cancel = context.WithCancel(context.Background())

	kubeconfig = os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		kubeconfig = homeDir + "/.kube/config"
	}

	ginkgo.By("loading kubeconfig from: " + kubeconfig)

	_, err := os.Stat(kubeconfig)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "kubeconfig file should exist at "+kubeconfig)

	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfg).NotTo(gomega.BeNil())

	err = mcpv1alpha1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = corev1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())

	ginkgo.By("connected to Kubernetes cluster successfully")
})

var _ = ginkgo.AfterSuite(func() {
	ginkgo.By("tearing down the test environment")
	cancel()
})

// JustAfterEach captures Kubernetes state immediately when a spec fails.
var _ = ginkgo.JustAfterEach(func() {
	if ginkgo.CurrentSpecReport().Failed() {
		dumpK8sState("SPEC FAILED - CAPTURING STATE BEFORE CLEANUP")
	}
})

func dumpK8sState(header string) {
	ginkgo.GinkgoWriter.Println("\n" + strings.Repeat("=", 80))
	ginkgo.GinkgoWriter.Println(header)
	ginkgo.GinkgoWriter.Println(strings.Repeat("=", 80))

	namespace := "default"
	dumpMCPServers(namespace)
	dumpMCPRemoteProxies(namespace)
	dumpConfigMaps(namespace)
	dumpEvents(namespace)

	ginkgo.GinkgoWriter.Println(strings.Repeat("=", 80))
	ginkgo.GinkgoWriter.Println("END OF STATE DUMP")
	ginkgo.GinkgoWriter.Println(strings.Repeat("=", 80) + "\n")
}

func dumpMCPServers(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- MCPServers ---")
	mcpList := &mcpv1alpha1.MCPServerList{}
	if err := k8sClient.List(ctx, mcpList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list MCPServers: %v\n", err)
		return
	}
	for _, mcp := range mcpList.Items {
		ginkgo.GinkgoWriter.Printf("  %s: Phase=%s Message=%s\n", mcp.Name, mcp.Status.Phase, mcp.Status.Message)
		for _, cond := range mcp.Status.Conditions {
			ginkgo.GinkgoWriter.Printf("    Condition %s: %s (%s) - %s\n", cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpMCPRemoteProxies(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- MCPRemoteProxies ---")
	proxyList := &mcpv1alpha1.MCPRemoteProxyList{}
	if err := k8sClient.List(ctx, proxyList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list MCPRemoteProxies: %v\n", err)
		return
	}
	for _, proxy := range proxyList.Items {
		ginkgo.GinkgoWriter.Printf("  %s: Phase=%s Message=%s\n", proxy.Name, proxy.Status.Phase, proxy.Status.Message)
		for _, cond := range proxy.Status.Conditions {
			ginkgo.GinkgoWriter.Printf("    Condition %s: %s (%s) - %s\n", cond.Type, cond.Status, cond.Reason, cond.Message)
		}
	}
}

func dumpConfigMaps(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- ConfigMaps (runconfig) ---")
	cmList := &corev1.ConfigMapList{}
	if err := k8sClient.List(ctx, cmList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list ConfigMaps: %v\n", err)
		return
	}
	for _, cm := range cmList.Items {
		if strings.HasSuffix(cm.Name, "-runconfig") {
			ginkgo.GinkgoWriter.Printf("  %s: keys=%v\n", cm.Name, keysOf(cm.Data))
		}
	}
}

func dumpEvents(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- Recent Events (last 20) ---")
	eventList := &corev1.EventList{}
	if err := k8sClient.List(ctx, eventList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list events: %v\n", err)
		return
	}

	events := eventList.Items
	if len(events) > 20 {
		events = events[len(events)-20:]
	}

	for _, event := range events {
		ginkgo.GinkgoWriter.Printf("  [%s] %s/%s: %s - %s\n",
			event.Type, event.InvolvedObject.Kind, event.InvolvedObject.Name,
			event.Reason, event.Message)
	}
}

func keysOf(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
