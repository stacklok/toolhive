// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"encoding/json"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// podPatchFromArgs extracts the --k8s-pod-patch JSON from the proxy container args.
func podPatchFromArgs(args []string) map[string]interface{} {
	for _, arg := range args {
		if patchJSON, ok := strings.CutPrefix(arg, "--k8s-pod-patch="); ok {
			var patch map[string]interface{}
			ExpectWithOffset(1, json.Unmarshal([]byte(patchJSON), &patch)).To(Succeed())
			return patch
		}
	}
	return nil
}

// sentinelEnvValues returns the credentialEnvName → value map of sentinel env vars
// on the mcp container of the pod patch.
func sentinelEnvValues(patch map[string]interface{}) map[string]string {
	result := map[string]string{}
	if patch == nil {
		return result
	}
	spec, ok := patch["spec"].(map[string]interface{})
	if !ok {
		return result
	}
	containers, ok := spec["containers"].([]interface{})
	if !ok {
		return result
	}
	for _, c := range containers {
		container, ok := c.(map[string]interface{})
		if !ok || container["name"] != "mcp" {
			continue
		}
		envs, ok := container["env"].([]interface{})
		if !ok {
			continue
		}
		for _, e := range envs {
			env, ok := e.(map[string]interface{})
			if !ok {
				continue
			}
			name, _ := env["name"].(string)
			value, _ := env["value"].(string)
			result[name] = value
		}
	}
	return result
}

var _ = Describe("MCPServer untrusted sentinel injection", Label("k8s", "untrusted", "sentinel"), func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	Context("When an untrusted MCPServer declares an egressPolicy", Ordered, func() {
		var (
			mcpServerName string
			mcpServer     *mcpv1beta1.MCPServer
		)

		BeforeAll(func() {
			mcpServerName = "untrusted-sentinel-it"
			mcpServer = &mcpv1beta1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      mcpServerName,
					Namespace: "default",
				},
				Spec: mcpv1beta1.MCPServerSpec{
					Image:     "example/mcp-server:latest",
					Transport: "stdio",
					Untrusted: true,
					GroupRef:  &mcpv1beta1.MCPGroupRef{Name: "test-group"},
					EgressPolicy: &mcpv1beta1.EgressPolicy{
						Providers: []mcpv1beta1.ProviderEgress{{
							Provider:          "github",
							AllowedHosts:      []string{"api.github.com"},
							CredentialEnvName: "GITHUB_TOKEN",
						}},
					},
				},
			}
			Expect(untrustedK8sClient.Create(untrustedCtx, mcpServer)).To(Succeed())
		})

		AfterAll(func() {
			Expect(untrustedK8sClient.Delete(untrustedCtx, mcpServer)).To(Succeed())
		})

		It("Should inject the sentinel env literal into the backend pod patch", func() {
			deployment := &appsv1.Deployment{}
			Eventually(func() error {
				return untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: "default",
				}, deployment)
			}, timeout, interval).Should(Succeed())

			env := sentinelEnvValues(podPatchFromArgs(deployment.Spec.Template.Spec.Containers[0].Args))
			Expect(env).To(HaveKeyWithValue("GITHUB_TOKEN", "thv-untrusted-sentinel:github"))
		})

		It("Should stop injecting the sentinel after an untrusted→trusted flip", func() {
			// Flip the workload to trusted.
			current := &mcpv1beta1.MCPServer{}
			Expect(untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
				Name:      mcpServerName,
				Namespace: "default",
			}, current)).To(Succeed())
			current.Spec.Untrusted = false
			Expect(untrustedK8sClient.Update(untrustedCtx, current)).To(Succeed())

			deployment := &appsv1.Deployment{}
			Eventually(func() map[string]string {
				if err := untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
					Name:      mcpServerName,
					Namespace: "default",
				}, deployment); err != nil {
					return map[string]string{"__error__": err.Error()}
				}
				return sentinelEnvValues(podPatchFromArgs(deployment.Spec.Template.Spec.Containers[0].Args))
			}, timeout, interval).ShouldNot(HaveKey("GITHUB_TOKEN"),
				"trusted reconcile must rebuild the pod patch without sentinel env")
		})
	})
})

var _ = Describe("MCPServer untrusted gate via spec field", Label("k8s", "untrusted", "gate"), func() {
	It("Should be inert for a trusted MCPServer carrying the interim Wave-0 annotation", func() {
		server := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "untrusted-annotation-inert",
				Namespace: "default",
				Annotations: map[string]string{
					"toolhive.stacklok.dev/untrusted": "true",
				},
			},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:     "example/mcp-server:latest",
				Transport: "stdio",
				// spec.secrets on a trusted workload is fine (annotation must not arm the gate).
				Secrets: []mcpv1beta1.SecretRef{{Name: "creds", Key: "token", TargetEnvName: "API_TOKEN"}},
			},
		}
		Expect(untrustedK8sClient.Create(untrustedCtx, server)).To(Succeed())
		defer func() {
			Expect(untrustedK8sClient.Delete(untrustedCtx, server)).To(Succeed())
		}()

		// The gate would latch Valid=False; assert it never does.
		Consistently(func() bool {
			current := &mcpv1beta1.MCPServer{}
			if err := untrustedK8sClient.Get(untrustedCtx, types.NamespacedName{
				Name:      server.Name,
				Namespace: "default",
			}, current); err != nil {
				return false
			}
			return !meta_FindFalseCondition(current.Status.Conditions, mcpv1beta1.ConditionTypeValid)
		}, 5*time.Second, 250*time.Millisecond).Should(BeTrue(),
			"the interim Wave-0 annotation must not arm the untrusted gate")
	})
})

func meta_FindFalseCondition(conditions []metav1.Condition, condType string) bool {
	for _, c := range conditions {
		if c.Type == condType && c.Status == metav1.ConditionFalse {
			return true
		}
	}
	return false
}
