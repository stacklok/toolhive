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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/test/e2e/images"
)

// EnsureRedis creates a Redis Deployment and Service if they don't already exist,
// then waits for Redis to be ready. Safe to call concurrently from multiple test blocks.
func EnsureRedis(ctx context.Context, c client.Client, namespace string, timeout, pollingInterval time.Duration) {
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
	if err := c.Create(ctx, deployment); err != nil && !apierrors.IsAlreadyExists(err) {
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}

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
	if err := c.Create(ctx, service); err != nil && !apierrors.IsAlreadyExists(err) {
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}

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

// SendAuthenticatedToolCall sends a JSON-RPC tools/call request with a Bearer token.
// Returns the HTTP status code, response body, and the Retry-After header value (empty if not set).
func SendAuthenticatedToolCall(
	httpClient *http.Client, port int32, toolName string, requestID int, bearerToken string,
) (int, []byte, string) {
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
	req.Header.Set("Authorization", "Bearer "+bearerToken)

	resp, err := httpClient.Do(req)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	retryAfter := resp.Header.Get("Retry-After")

	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	return resp.StatusCode, respBody, retryAfter
}

// SendInitialize sends a JSON-RPC initialize request and returns the session ID
// from the Mcp-Session header. This must be called before tools/call when auth is enabled.
func SendInitialize(
	httpClient *http.Client, port int32, bearerToken string,
) (sessionID string) {
	reqBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      0,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "e2e-test",
				"version": "1.0.0",
			},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	url := fmt.Sprintf("http://localhost:%d/mcp", port)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(bodyBytes))
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	resp, err := httpClient.Do(req)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	gomega.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK),
		"initialize should succeed")

	sessionID = resp.Header.Get("Mcp-Session-Id")
	gomega.Expect(sessionID).ToNot(gomega.BeEmpty(),
		"initialize response should include Mcp-Session-Id header")

	return sessionID
}

// SendAuthenticatedToolCallWithSession sends a JSON-RPC tools/call with Bearer token and session ID.
func SendAuthenticatedToolCallWithSession(
	httpClient *http.Client, port int32, toolName string, requestID int, bearerToken, sessionID string,
) (int, []byte, string) {
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
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}

	resp, err := httpClient.Do(req)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	retryAfter := resp.Header.Get("Retry-After")

	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	return resp.StatusCode, respBody, retryAfter
}

// GetOIDCToken fetches a JWT from the mock OIDC server for the given subject.
func GetOIDCToken(httpClient *http.Client, oidcNodePort int32, subject string) string {
	url := fmt.Sprintf("http://localhost:%d/token?subject=%s", oidcNodePort, subject)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, nil)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	resp, err := httpClient.Do(req)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	gomega.Expect(json.NewDecoder(resp.Body).Decode(&tokenResp)).To(gomega.Succeed())
	gomega.Expect(tokenResp.AccessToken).ToNot(gomega.BeEmpty(), "OIDC server should return a token")

	return tokenResp.AccessToken
}
