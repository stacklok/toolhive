// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package acceptancetests

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/test/e2e/images"
)

// DeployRedis creates a Redis Deployment and Service in the given namespace.
// No password is configured — matches the default empty THV_SESSION_REDIS_PASSWORD.
func DeployRedis(ctx context.Context, c client.Client, namespace string, timeout, pollingInterval time.Duration) {
	labels := map[string]string{"app": "redis"}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis",
			Namespace: namespace,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "redis",
							Image: images.RedisImage,
							Ports: []corev1.ContainerPort{{ContainerPort: 6379}},
						},
					},
				},
			},
		},
	}
	gomega.Expect(c.Create(ctx, deployment)).To(gomega.Succeed())

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "redis",
			Namespace: namespace,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Port: 6379, TargetPort: intstr.FromInt32(6379)},
			},
		},
	}
	gomega.Expect(c.Create(ctx, service)).To(gomega.Succeed())

	ginkgo.By("Waiting for Redis to be ready")
	gomega.Eventually(func() error {
		podList := &corev1.PodList{}
		if err := c.List(ctx, podList,
			client.InNamespace(namespace),
			client.MatchingLabels(labels)); err != nil {
			return err
		}
		for _, pod := range podList.Items {
			if pod.Status.Phase == corev1.PodRunning {
				for _, cond := range pod.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("redis pod not ready")
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// CleanupRedis deletes the Redis Deployment and Service.
func CleanupRedis(ctx context.Context, c client.Client, namespace string) {
	_ = c.Delete(ctx, &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: namespace},
	})
	_ = c.Delete(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "redis", Namespace: namespace},
	})
}

// SendToolCall sends a JSON-RPC tools/call request and returns the HTTP status code and body.
func SendToolCall(httpClient *http.Client, port int32, toolName string, requestID int) (int, []byte) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      requestID,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      toolName,
			"arguments": map[string]any{"input": "test"},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	url := fmt.Sprintf("http://localhost:%d/mcp", port)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := httpClient.Do(req)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	return resp.StatusCode, respBody
}
