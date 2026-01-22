// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package virtualmcp contains e2e tests for VirtualMCPServer against a real Kubernetes cluster
package virtualmcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
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

const (
	// fetchToolName is the name of the fetch tool used in tests
	fetchToolName = "fetch"
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
	// Show verbose output for e2e tests
	reporterConfig.Verbose = true

	ginkgo.RunSpecs(t, "VirtualMCPServer E2E Test Suite", suiteConfig, reporterConfig)
}

var _ = ginkgo.BeforeSuite(func() {
	logLevel := zapcore.InfoLevel
	logf.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true), zap.Level(logLevel)))

	ctx, cancel = context.WithCancel(context.Background())

	// Get kubeconfig path from environment or default
	kubeconfig = os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		homeDir, err := os.UserHomeDir()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		kubeconfig = homeDir + "/.kube/config"
	}

	ginkgo.By("loading kubeconfig from: " + kubeconfig)

	// Check if kubeconfig file exists
	_, err := os.Stat(kubeconfig)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "kubeconfig file should exist at "+kubeconfig)

	// Build config from kubeconfig
	cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfg).NotTo(gomega.BeNil())

	// Register schemes
	err = mcpv1alpha1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = appsv1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = corev1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = rbacv1.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Create Kubernetes client
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())

	ginkgo.By("connected to Kubernetes cluster successfully")
})

var _ = ginkgo.AfterSuite(func() {
	ginkgo.By("tearing down the test environment")
	cancel()
})

// JustAfterEach captures Kubernetes state immediately when a spec fails
// This runs before AfterEach/AfterAll cleanup, so resources still exist
var _ = ginkgo.JustAfterEach(func() {
	if ginkgo.CurrentSpecReport().Failed() {
		dumpK8sState("SPEC FAILED - CAPTURING STATE BEFORE CLEANUP")
	}
})

func dumpK8sState(header string) {
	ginkgo.GinkgoWriter.Println("\n" + strings.Repeat("=", 80))
	ginkgo.GinkgoWriter.Println("🔴 " + header)
	ginkgo.GinkgoWriter.Println(strings.Repeat("=", 80))

	namespace := "default"
	dumpVirtualMCPServers(namespace)
	dumpMCPServers(namespace)
	dumpPods(namespace)
	dumpServices(namespace)
	dumpEvents(namespace)
	dumpOperatorLogs()

	ginkgo.GinkgoWriter.Println(strings.Repeat("=", 80))
	ginkgo.GinkgoWriter.Println("END OF STATE DUMP")
	ginkgo.GinkgoWriter.Println(strings.Repeat("=", 80) + "\n")
}

func dumpVirtualMCPServers(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- VirtualMCPServers ---")
	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := k8sClient.List(ctx, vmcpList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list VirtualMCPServers: %v\n", err)
		return
	}
	for _, vmcp := range vmcpList.Items {
		ginkgo.GinkgoWriter.Printf("  %s: Phase=%s\n", vmcp.Name, vmcp.Status.Phase)
		for _, cond := range vmcp.Status.Conditions {
			ginkgo.GinkgoWriter.Printf("    Condition %s: %s (%s)\n", cond.Type, cond.Status, cond.Message)
		}
	}
}

func dumpMCPServers(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- MCPServers ---")
	mcpList := &mcpv1alpha1.MCPServerList{}
	if err := k8sClient.List(ctx, mcpList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list MCPServers: %v\n", err)
		return
	}
	for _, mcp := range mcpList.Items {
		ginkgo.GinkgoWriter.Printf("  %s: Phase=%s\n", mcp.Name, mcp.Status.Phase)
	}
}

func dumpPods(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- Pods ---")
	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list pods: %v\n", err)
		return
	}
	for _, pod := range podList.Items {
		// Focus on test-related pods
		if !strings.Contains(pod.Name, "vmcp") &&
			!strings.Contains(pod.Name, "backend") &&
			!strings.Contains(pod.Name, "mock") &&
			!strings.Contains(pod.Name, "yardstick") {
			continue
		}

		ginkgo.GinkgoWriter.Printf("\n  Pod: %s\n", pod.Name)
		ginkgo.GinkgoWriter.Printf("    Phase: %s\n", pod.Status.Phase)
		ginkgo.GinkgoWriter.Printf("    Ready: %v\n", isPodReady(&pod))

		// Pod conditions - shows why pod is not ready
		ginkgo.GinkgoWriter.Println("    Conditions:")
		for _, cond := range pod.Status.Conditions {
			status := string(cond.Status)
			msg := ""
			if cond.Message != "" {
				msg = fmt.Sprintf(" - %s", cond.Message)
			}
			if cond.Reason != "" {
				msg = fmt.Sprintf(" (%s)%s", cond.Reason, msg)
			}
			ginkgo.GinkgoWriter.Printf("      %s: %s%s\n", cond.Type, status, msg)
		}

		// Container statuses and readiness probe config
		for _, cs := range pod.Status.ContainerStatuses {
			ginkgo.GinkgoWriter.Printf("    Container %s: Ready=%v, RestartCount=%d, Started=%v\n",
				cs.Name, cs.Ready, cs.RestartCount, cs.Started != nil && *cs.Started)
			if cs.State.Waiting != nil {
				ginkgo.GinkgoWriter.Printf("      State: Waiting - %s: %s\n",
					cs.State.Waiting.Reason, cs.State.Waiting.Message)
			}
			if cs.State.Running != nil {
				ginkgo.GinkgoWriter.Printf("      State: Running since %s\n",
					cs.State.Running.StartedAt.Format("15:04:05"))
			}
			if cs.State.Terminated != nil {
				ginkgo.GinkgoWriter.Printf("      State: Terminated - %s (exit %d): %s\n",
					cs.State.Terminated.Reason, cs.State.Terminated.ExitCode, cs.State.Terminated.Message)
			}

			// Find container spec for readiness probe info
			for _, containerSpec := range pod.Spec.Containers {
				if containerSpec.Name == cs.Name && containerSpec.ReadinessProbe != nil {
					probe := containerSpec.ReadinessProbe
					ginkgo.GinkgoWriter.Printf("      ReadinessProbe: InitialDelay=%ds, Period=%ds, Timeout=%ds, Failure=%d\n",
						probe.InitialDelaySeconds, probe.PeriodSeconds, probe.TimeoutSeconds, probe.FailureThreshold)
					if probe.HTTPGet != nil {
						ginkgo.GinkgoWriter.Printf("        HTTPGet: %s:%v%s\n",
							probe.HTTPGet.Scheme, probe.HTTPGet.Port.String(), probe.HTTPGet.Path)
					}
					if probe.TCPSocket != nil {
						ginkgo.GinkgoWriter.Printf("        TCPSocket: port %v\n", probe.TCPSocket.Port.String())
					}
					if probe.Exec != nil {
						ginkgo.GinkgoWriter.Printf("        Exec: %v\n", probe.Exec.Command)
					}
				}
			}
		}

		// Get pod logs (last 50 lines) - try current first, then previous if container crashed
		for _, container := range pod.Spec.Containers {
			logs, err := getPodLogs(ctx, namespace, pod.Name, container.Name, false)
			logType := "current"
			if err != nil {
				// Try previous logs if current fails (container may have crashed)
				logs, err = getPodLogs(ctx, namespace, pod.Name, container.Name, true)
				logType = "previous"
			}
			if err != nil {
				ginkgo.GinkgoWriter.Printf("    Logs (%s): failed to get: %v\n", container.Name, err)
			} else if logs != "" {
				ginkgo.GinkgoWriter.Printf("    Logs (%s) [%s, last 50 lines]:\n", container.Name, logType)
				// Indent logs
				for _, line := range strings.Split(logs, "\n") {
					if line != "" {
						ginkgo.GinkgoWriter.Printf("      %s\n", line)
					}
				}
			}
		}
	}
}

func dumpServices(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- Services ---")
	svcList := &corev1.ServiceList{}
	if err := k8sClient.List(ctx, svcList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list services: %v\n", err)
		return
	}
	for _, svc := range svcList.Items {
		// Focus on test-related services
		if !strings.Contains(svc.Name, "vmcp") &&
			!strings.Contains(svc.Name, "backend") &&
			!strings.Contains(svc.Name, "mock") {
			continue
		}
		ports := []string{}
		for _, p := range svc.Spec.Ports {
			if p.NodePort > 0 {
				ports = append(ports, fmt.Sprintf("%d->%d(NodePort:%d)", p.Port, p.TargetPort.IntValue(), p.NodePort))
			} else {
				ports = append(ports, fmt.Sprintf("%d->%d", p.Port, p.TargetPort.IntValue()))
			}
		}
		ginkgo.GinkgoWriter.Printf("  %s: Type=%s, Ports=%s\n", svc.Name, svc.Spec.Type, strings.Join(ports, ", "))
	}
}

func dumpEvents(namespace string) {
	ginkgo.GinkgoWriter.Println("\n--- Recent Events (last 20) ---")
	eventList := &corev1.EventList{}
	if err := k8sClient.List(ctx, eventList, client.InNamespace(namespace)); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list events: %v\n", err)
		return
	}

	// Get last 20 events
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

func dumpOperatorLogs() {
	ginkgo.GinkgoWriter.Println("\n--- Operator Logs (filtered for errors and VirtualMCPServer) ---")

	// List operator pods in toolhive-system namespace
	podList := &corev1.PodList{}
	if err := k8sClient.List(ctx, podList,
		client.InNamespace("toolhive-system"),
		client.MatchingLabels{"app.kubernetes.io/name": "toolhive-operator"}); err != nil {
		ginkgo.GinkgoWriter.Printf("Failed to list operator pods: %v\n", err)
		return
	}

	if len(podList.Items) == 0 {
		ginkgo.GinkgoWriter.Println("  No operator pods found in toolhive-system namespace")
		return
	}

	for _, pod := range podList.Items {
		ginkgo.GinkgoWriter.Printf("\n  Operator Pod: %s (Phase: %s)\n", pod.Name, pod.Status.Phase)

		// Get logs from the main container (typically named "manager" or first container)
		for _, container := range pod.Spec.Containers {
			logs, err := getPodLogs(ctx, "toolhive-system", pod.Name, container.Name, false)
			if err != nil {
				ginkgo.GinkgoWriter.Printf("    Logs (%s): failed to get: %v\n", container.Name, err)
				continue
			}

			if logs == "" {
				ginkgo.GinkgoWriter.Printf("    Logs (%s): no logs available\n", container.Name)
				continue
			}

			ginkgo.GinkgoWriter.Printf("    Logs (%s) [filtered]:\n", container.Name)
			// Filter for relevant log lines (errors, warnings, and virtualmcpserver reconciliation)
			lineCount := 0
			for _, line := range strings.Split(logs, "\n") {
				if line == "" {
					continue
				}
				// Show error/warning logs or logs related to virtualmcpserver reconciliation
				lineLower := strings.ToLower(line)
				if strings.Contains(lineLower, "error") ||
					strings.Contains(lineLower, "warning") ||
					strings.Contains(lineLower, "virtualmcpserver") ||
					strings.Contains(lineLower, "failed") ||
					strings.Contains(lineLower, "vmcp") {
					ginkgo.GinkgoWriter.Printf("      %s\n", line)
					lineCount++
				}
			}
			if lineCount == 0 {
				ginkgo.GinkgoWriter.Println("      (no matching log lines found)")
			}
		}
	}
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
