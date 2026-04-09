// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package authserver provides helper functions for authServerRef E2E tests.
package authserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// WaitForMCPServerPhase polls until an MCPServer reaches the expected phase.
// It stops early if the server enters Failed phase when not expecting failure.
func WaitForMCPServerPhase(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	expectedPhase mcpv1alpha1.MCPServerPhase,
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
		if server.Status.Phase == expectedPhase {
			return nil
		}
		// Stop early if we reached Failed when expecting success
		if expectedPhase != mcpv1alpha1.MCPServerPhaseFailed &&
			server.Status.Phase == mcpv1alpha1.MCPServerPhaseFailed {
			return gomega.StopTrying(fmt.Sprintf(
				"MCPServer %s entered Failed phase unexpectedly: %s", name, server.Status.Message))
		}
		return fmt.Errorf("MCPServer %s phase is %s, waiting for %s",
			name, server.Status.Phase, expectedPhase)
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// WaitForMCPRemoteProxyPhase polls until an MCPRemoteProxy reaches the expected phase.
// It stops early if the proxy enters Failed phase when not expecting failure.
func WaitForMCPRemoteProxyPhase(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	expectedPhase mcpv1alpha1.MCPRemoteProxyPhase,
	timeout, pollingInterval time.Duration,
) {
	gomega.Eventually(func() error {
		proxy := &mcpv1alpha1.MCPRemoteProxy{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, proxy); err != nil {
			return err
		}
		if proxy.Status.Phase == expectedPhase {
			return nil
		}
		// Stop early if we reached Failed when expecting success
		if expectedPhase != mcpv1alpha1.MCPRemoteProxyPhaseFailed &&
			proxy.Status.Phase == mcpv1alpha1.MCPRemoteProxyPhaseFailed {
			return gomega.StopTrying(fmt.Sprintf(
				"MCPRemoteProxy %s entered Failed phase unexpectedly: %s", name, proxy.Status.Message))
		}
		return fmt.Errorf("MCPRemoteProxy %s phase is %s, waiting for %s",
			name, proxy.Status.Phase, expectedPhase)
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// GetRunConfigFromConfigMap fetches the runconfig ConfigMap for a resource and
// returns the parsed JSON as a map.
func GetRunConfigFromConfigMap(
	ctx context.Context,
	c client.Client,
	resourceName, namespace string,
) (map[string]interface{}, error) {
	configMapName := fmt.Sprintf("%s-runconfig", resourceName)
	cm := &corev1.ConfigMap{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: namespace,
	}, cm); err != nil {
		return nil, fmt.Errorf("failed to get ConfigMap %s: %w", configMapName, err)
	}

	runConfigJSON, ok := cm.Data["runconfig.json"]
	if !ok {
		return nil, fmt.Errorf("ConfigMap %s does not contain runconfig.json key", configMapName)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(runConfigJSON), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal runconfig.json: %w", err)
	}

	return result, nil
}

// ExpectMCPServerConditionMessage waits for an MCPServer to have a condition
// whose message contains the given substring and whose Status matches expectedStatus.
func ExpectMCPServerConditionMessage(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	conditionType string,
	expectedStatus metav1.ConditionStatus,
	messageSubstring string,
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
		for _, cond := range server.Status.Conditions {
			if cond.Type == conditionType {
				if cond.Status != expectedStatus {
					return fmt.Errorf("condition %s status is %s, expected %s (message: %s)",
						conditionType, cond.Status, expectedStatus, cond.Message)
				}
				if strings.Contains(cond.Message, messageSubstring) {
					return nil
				}
				return fmt.Errorf("condition %s message %q does not contain %q",
					conditionType, cond.Message, messageSubstring)
			}
		}
		return fmt.Errorf("condition %s not found on MCPServer %s", conditionType, name)
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// ExpectMCPRemoteProxyConditionMessage waits for an MCPRemoteProxy to have a condition
// whose message contains the given substring and whose Status matches expectedStatus.
func ExpectMCPRemoteProxyConditionMessage(
	ctx context.Context,
	c client.Client,
	name, namespace string,
	conditionType string,
	expectedStatus metav1.ConditionStatus,
	messageSubstring string,
	timeout, pollingInterval time.Duration,
) {
	gomega.Eventually(func() error {
		proxy := &mcpv1alpha1.MCPRemoteProxy{}
		if err := c.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: namespace,
		}, proxy); err != nil {
			return err
		}
		for _, cond := range proxy.Status.Conditions {
			if cond.Type == conditionType {
				if cond.Status != expectedStatus {
					return fmt.Errorf("condition %s status is %s, expected %s (message: %s)",
						conditionType, cond.Status, expectedStatus, cond.Message)
				}
				if strings.Contains(cond.Message, messageSubstring) {
					return nil
				}
				return fmt.Errorf("condition %s message %q does not contain %q",
					conditionType, cond.Message, messageSubstring)
			}
		}
		return fmt.Errorf("condition %s not found on MCPRemoteProxy %s", conditionType, name)
	}, timeout, pollingInterval).Should(gomega.Succeed())
}

// deleteIgnoreNotFound deletes a resource and ignores NotFound errors.
func deleteIgnoreNotFound(ctx context.Context, c client.Client, obj client.Object) {
	err := c.Delete(ctx, obj)
	if err != nil && !apierrors.IsNotFound(err) {
		ginkgo.GinkgoWriter.Printf("Warning: failed to delete %T %s: %v\n",
			obj, obj.GetName(), err)
	}
}

// newEmbeddedAuthConfig creates an MCPExternalAuthConfig with type embeddedAuthServer.
func newEmbeddedAuthConfig(name, namespace string) *mcpv1alpha1.MCPExternalAuthConfig {
	return &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1alpha1.EmbeddedAuthServerConfig{
				Issuer: "http://localhost:9090",
				UpstreamProviders: []mcpv1alpha1.UpstreamProviderConfig{
					{
						Name: "test-provider",
						Type: mcpv1alpha1.UpstreamProviderTypeOIDC,
						OIDCConfig: &mcpv1alpha1.OIDCUpstreamConfig{
							IssuerURL: "https://accounts.google.com",
							ClientID:  "test-client-id",
						},
					},
				},
			},
		},
	}
}

// newUnauthenticatedConfig creates an MCPExternalAuthConfig with type unauthenticated.
func newUnauthenticatedConfig(name, namespace string) *mcpv1alpha1.MCPExternalAuthConfig {
	return &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeUnauthenticated,
		},
	}
}

// newMCPOIDCConfig creates an MCPOIDCConfig with inline OIDC configuration.
func newMCPOIDCConfig(name, namespace string) *mcpv1alpha1.MCPOIDCConfig {
	return &mcpv1alpha1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPOIDCConfigSpec{
			Type: mcpv1alpha1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1alpha1.InlineOIDCSharedConfig{
				Issuer: "http://localhost:9090",
			},
		},
	}
}

// newOIDCConfigRef creates an MCPOIDCConfigReference for use on MCPServer/MCPRemoteProxy specs.
func newOIDCConfigRef(oidcConfigName string) *mcpv1alpha1.MCPOIDCConfigReference {
	return &mcpv1alpha1.MCPOIDCConfigReference{
		Name:     oidcConfigName,
		Audience: "https://test-resource.example.com",
	}
}

// newAWSStsConfig creates an MCPExternalAuthConfig with type awsSts.
func newAWSStsConfig(name, namespace string) *mcpv1alpha1.MCPExternalAuthConfig {
	return &mcpv1alpha1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: mcpv1alpha1.MCPExternalAuthConfigSpec{
			Type: mcpv1alpha1.ExternalAuthTypeAWSSts,
			AWSSts: &mcpv1alpha1.AWSStsConfig{
				Region:          "us-east-1",
				FallbackRoleArn: "arn:aws:iam::123456789012:role/test-role",
			},
		},
	}
}
