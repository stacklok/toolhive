// SPDX-License-Identifier: Apache-2.0

// Package controllers contains integration tests for the EmbeddingServer controller.
package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// UpdateTestCase defines a test case for EmbeddingServer update scenarios.
type UpdateTestCase struct {
	Name         string
	InitialState *mcpv1alpha1.EmbeddingServer
	Updates      []UpdateStep
}

// UpdateStep defines a single update operation and its expected result.
type UpdateStep struct {
	Name        string
	ApplyUpdate func(es *mcpv1alpha1.EmbeddingServer)
	// Expected StatefulSet state after the update (nil means expect no changes)
	ExpectedStatefulSet *appsv1.StatefulSet
	// Expected Service state after the update (nil means expect no changes)
	ExpectedService *corev1.Service
}

var _ = Describe("EmbeddingServer Controller Update Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
	)

	// Define update test cases
	updateTestCases := []UpdateTestCase{
		{
			Name: "When updating EmbeddingServer image",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-image",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:v1.0",
					Port:  8080,
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet when image changes to v2.0",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Image = "ghcr.io/huggingface/text-embeddings-inference:v2.0"
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Image: "ghcr.io/huggingface/text-embeddings-inference:v2.0",
									}},
								},
							},
						},
					},
				},
				{
					Name: "Should update StatefulSet when image changes to v3.0",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Image = "ghcr.io/huggingface/text-embeddings-inference:v3.0"
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Image: "ghcr.io/huggingface/text-embeddings-inference:v3.0",
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer replicas",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-replicas",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model:    "sentence-transformers/all-MiniLM-L6-v2",
					Image:    "ghcr.io/huggingface/text-embeddings-inference:latest",
					Port:     8080,
					Replicas: ptr.To(int32(1)),
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should scale up to 3 replicas",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Replicas = ptr.To(int32(3))
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Replicas: ptr.To(int32(3)),
						},
					},
				},
				{
					Name: "Should scale down to 2 replicas",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Replicas = ptr.To(int32(2))
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Replicas: ptr.To(int32(2)),
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer model",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-model",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					Port:  8080,
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet args when model changes",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Model = "sentence-transformers/all-mpnet-base-v2"
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Args: []string{"--model-id", "sentence-transformers/all-mpnet-base-v2"},
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer environment variables",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-env",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					Port:  8080,
					Env: []mcpv1alpha1.EnvVar{
						{Name: "LOG_LEVEL", Value: "info"},
					},
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet when env var value changes",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Env = []mcpv1alpha1.EnvVar{
							{Name: "LOG_LEVEL", Value: "debug"},
						}
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Env: []corev1.EnvVar{{Name: "LOG_LEVEL"}},
									}},
								},
							},
						},
					},
				},
				{
					Name: "Should update StatefulSet when new env var is added",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Env = []mcpv1alpha1.EnvVar{
							{Name: "LOG_LEVEL", Value: "debug"},
							{Name: "NEW_VAR", Value: "new_value"},
						}
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Env: []corev1.EnvVar{
											{Name: "LOG_LEVEL"},
											{Name: "NEW_VAR"},
										},
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer port",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-port",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					Port:  8080,
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet and Service when port changes",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Port = 9090
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Args: []string{"--port", "9090"},
									}},
								},
							},
						},
					},
					ExpectedService: &corev1.Service{
						Spec: corev1.ServiceSpec{
							Ports: []corev1.ServicePort{{Port: 9090}},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer resources",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-resources",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					Resources: mcpv1alpha1.ResourceRequirements{
						Limits:   mcpv1alpha1.ResourceList{CPU: "1", Memory: "2Gi"},
						Requests: mcpv1alpha1.ResourceList{CPU: "500m", Memory: "1Gi"},
					},
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet when resource limits change",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Resources = mcpv1alpha1.ResourceRequirements{
							Limits:   mcpv1alpha1.ResourceList{CPU: "2", Memory: "4Gi"},
							Requests: mcpv1alpha1.ResourceList{CPU: "1", Memory: "2Gi"},
						}
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Resources: corev1.ResourceRequirements{
											Limits: corev1.ResourceList{
												corev1.ResourceCPU:    resource.MustParse("2"),
												corev1.ResourceMemory: resource.MustParse("4Gi"),
											},
											Requests: corev1.ResourceList{
												corev1.ResourceCPU:    resource.MustParse("1"),
												corev1.ResourceMemory: resource.MustParse("2Gi"),
											},
										},
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer args",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-args",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					Args:  []string{"--max-concurrent-requests", "256"},
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet when args change",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Args = []string{"--max-concurrent-requests", "512", "--tokenization-workers", "4"}
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Args: []string{"--max-concurrent-requests", "512", "--tokenization-workers", "4"},
									}},
								},
							},
						},
					},
				},
				{
					Name: "Should update StatefulSet when args are removed",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.Args = nil
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										Args: []string{"--model-id", "sentence-transformers/all-MiniLM-L6-v2"},
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer ImagePullPolicy",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-imagepullpolicy",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model:           "sentence-transformers/all-MiniLM-L6-v2",
					Image:           "ghcr.io/huggingface/text-embeddings-inference:latest",
					ImagePullPolicy: "IfNotPresent",
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet when ImagePullPolicy changes",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.ImagePullPolicy = "Always"
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						Spec: appsv1.StatefulSetSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{
										ImagePullPolicy: corev1.PullAlways,
									}},
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When updating EmbeddingServer ResourceOverrides",
			InitialState: &mcpv1alpha1.EmbeddingServer{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-update-resourceoverrides",
					Namespace: defaultNamespace,
				},
				Spec: mcpv1alpha1.EmbeddingServerSpec{
					Model: "sentence-transformers/all-MiniLM-L6-v2",
					Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
				},
			},
			Updates: []UpdateStep{
				{
					Name: "Should update StatefulSet when adding annotations",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.ResourceOverrides = &mcpv1alpha1.EmbeddingResourceOverrides{
							Deployment: &mcpv1alpha1.EmbeddingDeploymentOverrides{
								ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
									Annotations: map[string]string{"new-annotation": "new-value"},
								},
							},
						}
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{"new-annotation": "new-value"},
						},
					},
				},
				{
					Name: "Should update StatefulSet and Service when adding annotations to both",
					ApplyUpdate: func(es *mcpv1alpha1.EmbeddingServer) {
						es.Spec.ResourceOverrides = &mcpv1alpha1.EmbeddingResourceOverrides{
							Deployment: &mcpv1alpha1.EmbeddingDeploymentOverrides{
								ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
									Annotations: map[string]string{"new-annotation": "new-value"},
								},
							},
							Service: &mcpv1alpha1.ResourceMetadataOverrides{
								Annotations: map[string]string{"service-annotation": "service-value"},
							},
						}
					},
					ExpectedStatefulSet: &appsv1.StatefulSet{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{"new-annotation": "new-value"},
						},
					},
					ExpectedService: &corev1.Service{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{"service-annotation": "service-value"},
						},
					},
				},
			},
		},
	}

	// Helper to run a single update test case
	runUpdateTestCase := func(tc UpdateTestCase) {
		Context(tc.Name, Ordered, func() {
			var embeddingServer *mcpv1alpha1.EmbeddingServer

			BeforeAll(func() {
				_ = k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: tc.InitialState.Namespace}})
				embeddingServer = tc.InitialState.DeepCopy()
				Expect(k8sClient.Create(ctx, embeddingServer)).To(Succeed())
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), &appsv1.StatefulSet{})).To(Succeed())
				}, timeout, interval).Should(Succeed())
			})

			AfterAll(func() {
				_ = k8sClient.Delete(ctx, embeddingServer)
			})

			for _, update := range tc.Updates {
				update := update
				It(update.Name, func() {
					// Capture original state before update
					originalSts := &appsv1.StatefulSet{}
					Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), originalSts)).To(Succeed())
					originalSvc := &corev1.Service{}
					Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), originalSvc)).To(Succeed())

					// Apply the update
					Eventually(func(g Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), embeddingServer)).To(Succeed())
						update.ApplyUpdate(embeddingServer)
						g.Expect(k8sClient.Update(ctx, embeddingServer)).To(Succeed())
					}, timeout, interval).Should(Succeed())

					// Verify the StatefulSet matches expected state (nil means expect no changes)
					if update.ExpectedStatefulSet != nil {
						Eventually(func(g Gomega) {
							sts := &appsv1.StatefulSet{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), sts)).To(Succeed())
							verifyStatefulSetEqualsG(g, sts, update.ExpectedStatefulSet)
						}, timeout, interval).Should(Succeed())
					} else {
						// Verify StatefulSet hasn't changed
						Consistently(func(g Gomega) {
							sts := &appsv1.StatefulSet{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), sts)).To(Succeed())
							g.Expect(sts.Spec).To(Equal(originalSts.Spec))
						}, time.Second*2, interval).Should(Succeed())
					}

					// Verify the Service matches expected state (nil means expect no changes)
					if update.ExpectedService != nil {
						Eventually(func(g Gomega) {
							svc := &corev1.Service{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), svc)).To(Succeed())
							verifyServiceEqualsG(g, svc, update.ExpectedService)
						}, timeout, interval).Should(Succeed())
					} else {
						// Verify Service hasn't changed
						Consistently(func(g Gomega) {
							svc := &corev1.Service{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(embeddingServer), svc)).To(Succeed())
							g.Expect(svc.Spec).To(Equal(originalSvc.Spec))
						}, time.Second*2, interval).Should(Succeed())
					}
				})
			}
		})
	}

	// Run all update test cases
	for _, tc := range updateTestCases {
		runUpdateTestCase(tc)
	}
})
