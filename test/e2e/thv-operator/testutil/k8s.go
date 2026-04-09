// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package testutil provides shared helpers for operator E2E tests.
package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// CheckPodsReady checks that at least one pod matching the given labels is running and ready.
func CheckPodsReady(ctx context.Context, c client.Client, namespace string, labels map[string]string) error {
	podList := &corev1.PodList{}
	if err := c.List(ctx, podList,
		client.InNamespace(namespace),
		client.MatchingLabels(labels)); err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return fmt.Errorf("no pods found with labels %v", labels)
	}

	runningPods := 0
	for _, pod := range podList.Items {
		if pod.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range pod.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				runningPods++
				break
			}
		}
	}
	if runningPods == 0 {
		return fmt.Errorf("no running and ready pods found with labels %v", labels)
	}
	return nil
}

// GetPodLogs retrieves logs from a specific pod container.
func GetPodLogs(ctx context.Context, namespace, podName, containerName string, previous bool) (string, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeconfigPath := os.Getenv("KUBECONFIG")
		if kubeconfigPath == "" {
			kubeconfigPath = clientcmd.RecommendedHomeFile
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return "", fmt.Errorf("failed to get rest config: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return "", fmt.Errorf("failed to create clientset: %w", err)
	}

	tailLines := int64(50)
	req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{
		Container: containerName,
		Previous:  previous,
		TailLines: &tailLines,
	})

	stream, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get log stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	buf := new(bytes.Buffer)
	_, err = io.Copy(buf, stream)
	if err != nil {
		return "", fmt.Errorf("failed to read logs: %w", err)
	}
	return buf.String(), nil
}

// WaitForMCPServerRunning waits for an MCPServer to reach the Running phase.
func WaitForMCPServerRunning(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	timeout, pollingInterval time.Duration,
) {
	gomega.Eventually(func() error {
		server := &mcpv1alpha1.MCPServer{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, server); err != nil {
			return err
		}
		if server.Status.Phase == mcpv1alpha1.MCPServerPhaseFailed {
			return gomega.StopTrying(fmt.Sprintf("MCPServer %s failed: %s", name, server.Status.Message))
		}
		if server.Status.Phase != mcpv1alpha1.MCPServerPhaseReady {
			return fmt.Errorf("MCPServer %s not ready, phase: %s", name, server.Status.Phase)
		}
		return nil
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// CreateNodePortService creates a NodePort service targeting the MCPServer proxy pods.
func CreateNodePortService(ctx context.Context, c client.Client, serverName, namespace string) {
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serverName + "-nodeport",
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeNodePort,
			Selector: map[string]string{
				"app.kubernetes.io/name":     "mcpserver",
				"app.kubernetes.io/instance": serverName,
			},
			Ports: []corev1.ServicePort{
				{Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
			},
		},
	}
	gomega.Expect(c.Create(ctx, service)).To(gomega.Succeed())
}

// GetNodePort waits for a NodePort service to get a port assigned and returns it.
func GetNodePort(
	ctx context.Context,
	c client.Client,
	serviceName, namespace string,
	timeout, pollingInterval time.Duration,
) int32 {
	var nodePort int32

	gomega.Eventually(func() error {
		service := &corev1.Service{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      serviceName,
			Namespace: namespace,
		}, service); err != nil {
			return err
		}
		for _, port := range service.Spec.Ports {
			if port.NodePort > 0 {
				nodePort = port.NodePort
				return nil
			}
		}
		return fmt.Errorf("no NodePort assigned yet on service %s", serviceName)
	}, timeout, pollingInterval).Should(gomega.Succeed())

	return nodePort
}
