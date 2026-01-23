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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// TestCase defines a table-driven test case for EmbeddingServer controller
type TestCase struct {
	Name string
	// InitialState contains objects to create before running assertions
	InitialState InitialState
	// FinalState defines the expected Kubernetes state after reconciliation
	FinalState FinalState
}

// InitialState represents the initial Kubernetes objects to create
type InitialState struct {
	EmbeddingServer *mcpv1alpha1.EmbeddingServer
	Secrets         []*corev1.Secret
}

// FinalState represents the expected Kubernetes state after reconciliation
// Uses actual K8s objects for comparison - only non-nil/non-zero fields are checked
type FinalState struct {
	// StatefulSet expected state (nil means don't check specific fields)
	StatefulSet *appsv1.StatefulSet
	// Service expected state (nil means don't check specific fields)
	Service *corev1.Service
	// EmbeddingServer status expectations
	Status *mcpv1alpha1.EmbeddingServerStatus
}

var _ = Describe("EmbeddingServer Controller Integration Tests", func() {
	const (
		timeout          = time.Second * 30
		interval         = time.Millisecond * 250
		defaultNamespace = "default"
	)

	// Helper function to create test namespace
	createNamespace := func(namespace string) {
		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
			},
		}
		_ = k8sClient.Create(ctx, ns)
	}

	// Helper to run a single test case
	runTestCase := func(tc TestCase) {
		Context(tc.Name, Ordered, func() {
			var createdEmbeddingServer *mcpv1alpha1.EmbeddingServer

			BeforeAll(func() {
				namespace := tc.InitialState.EmbeddingServer.Namespace
				createNamespace(namespace)

				// Create secrets first
				for _, secret := range tc.InitialState.Secrets {
					Expect(k8sClient.Create(ctx, secret)).Should(Succeed())
				}

				// Create the EmbeddingServer
				Expect(k8sClient.Create(ctx, tc.InitialState.EmbeddingServer)).Should(Succeed())

				// Fetch the created resource to get UID etc.
				createdEmbeddingServer = &mcpv1alpha1.EmbeddingServer{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      tc.InitialState.EmbeddingServer.Name,
						Namespace: tc.InitialState.EmbeddingServer.Namespace,
					}, createdEmbeddingServer)
				}, timeout, interval).Should(Succeed())
			})

			AfterAll(func() {
				// Clean up EmbeddingServer
				if tc.InitialState.EmbeddingServer != nil {
					_ = k8sClient.Delete(ctx, tc.InitialState.EmbeddingServer)
				}
				// Clean up secrets
				for _, secret := range tc.InitialState.Secrets {
					_ = k8sClient.Delete(ctx, secret)
				}
			})

			// StatefulSet assertions
			It("Should create StatefulSet with expected configuration", func() {
				actual := &appsv1.StatefulSet{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      tc.InitialState.EmbeddingServer.Name,
						Namespace: tc.InitialState.EmbeddingServer.Namespace,
					}, actual)
				}, timeout, interval).Should(Succeed())

				if tc.FinalState.StatefulSet != nil {
					verifyStatefulSetEquals(actual, tc.FinalState.StatefulSet)
				}
				verifyOwnerReference(actual.OwnerReferences, createdEmbeddingServer, "StatefulSet")
			})

			// Service assertions
			It("Should create Service with expected configuration", func() {
				actual := &corev1.Service{}
				Eventually(func() error {
					return k8sClient.Get(ctx, types.NamespacedName{
						Name:      tc.InitialState.EmbeddingServer.Name,
						Namespace: tc.InitialState.EmbeddingServer.Namespace,
					}, actual)
				}, timeout, interval).Should(Succeed())

				if tc.FinalState.Service != nil {
					verifyServiceEquals(actual, tc.FinalState.Service)
				}
				verifyOwnerReference(actual.OwnerReferences, createdEmbeddingServer, "Service")
			})

			// Status assertions
			It("Should have expected status and finalizer", func() {
				Eventually(func() bool {
					actual := &mcpv1alpha1.EmbeddingServer{}
					err := k8sClient.Get(ctx, types.NamespacedName{
						Name:      tc.InitialState.EmbeddingServer.Name,
						Namespace: tc.InitialState.EmbeddingServer.Namespace,
					}, actual)
					if err != nil {
						return false
					}
					return verifyStatusEquals(actual, tc.FinalState.Status)
				}, timeout, interval).Should(BeTrue())
			})
		})
	}

	// Define test cases as a table using actual K8s objects
	testCases := []TestCase{
		{
			Name: "When creating an EmbeddingServer with minimal config (verifies defaults)",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-defaults",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						// Only required fields - model and image
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/name":       "embeddingserver",
							"app.kubernetes.io/instance":   "test-defaults",
							"app.kubernetes.io/component":  "embedding-server",
							"app.kubernetes.io/managed-by": "toolhive-operator",
						},
					},
					Spec: appsv1.StatefulSetSpec{
						// Default: 1 replica
						Replicas: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "embedding",
									Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
									// Default port: 8080
									Args: []string{"--model-id", "sentence-transformers/all-MiniLM-L6-v2", "--port", "8080"},
									Env:  []corev1.EnvVar{{Name: "MODEL_ID", Value: "sentence-transformers/all-MiniLM-L6-v2"}},
									// Default: IfNotPresent
									ImagePullPolicy: corev1.PullIfNotPresent,
									// Default: no resource limits or requests
									Resources: corev1.ResourceRequirements{},
									LivenessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}},
									},
									ReadinessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}},
									},
								}},
							},
						},
					},
				},
				// Default port: 8080
				Service: &corev1.Service{
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{{Port: 8080}},
					},
				},
				Status: &mcpv1alpha1.EmbeddingServerStatus{
					// URL uses default port
					URL: "http://test-defaults.default.svc.cluster.local:8080",
				},
			},
		},
		{
			Name: "When creating a basic EmbeddingServer",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-basic",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/name":       "embeddingserver",
							"app.kubernetes.io/instance":   "test-basic",
							"app.kubernetes.io/component":  "embedding-server",
							"app.kubernetes.io/managed-by": "toolhive-operator",
						},
					},
					Spec: appsv1.StatefulSetSpec{
						Replicas: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:  "embedding",
									Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
									Args:  []string{"--model-id", "sentence-transformers/all-MiniLM-L6-v2", "--port", "8080"},
									Env:   []corev1.EnvVar{{Name: "MODEL_ID", Value: "sentence-transformers/all-MiniLM-L6-v2"}},
									LivenessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}},
									},
									ReadinessProbe: &corev1.Probe{
										ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health"}},
									},
								}},
							},
						},
					},
				},
				Service: &corev1.Service{
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{{Port: 8080}},
					},
				},
				Status: &mcpv1alpha1.EmbeddingServerStatus{
					URL: "http://test-basic.default.svc.cluster.local:8080",
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with model cache enabled",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-with-cache",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						ModelCache: &mcpv1alpha1.ModelCacheConfig{
							Enabled: true,
							Size:    "20Gi",
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Replicas: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:         "embedding",
									Env:          []corev1.EnvVar{{Name: "HF_HOME", Value: "/data"}},
									VolumeMounts: []corev1.VolumeMount{{Name: "model-cache", MountPath: "/data"}},
								}},
							},
						},
						VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
							ObjectMeta: metav1.ObjectMeta{Name: "model-cache"},
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")},
								},
							},
						}},
					},
				},
				Service: &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8080}}}},
			},
		},
		{
			Name: "When creating an EmbeddingServer with resource requirements",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-resources",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						Resources: mcpv1alpha1.ResourceRequirements{
							Limits:   mcpv1alpha1.ResourceList{CPU: "2", Memory: "4Gi"},
							Requests: mcpv1alpha1.ResourceList{CPU: "500m", Memory: "1Gi"},
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "embedding",
									Resources: corev1.ResourceRequirements{
										Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("4Gi")},
										Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")},
									},
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with custom replicas",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-replicas",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model:    "sentence-transformers/all-MiniLM-L6-v2",
						Image:    "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:     8080,
						Replicas: ptr.To(int32(3)),
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Replicas: ptr.To(int32(3)),
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with invalid PodTemplateSpec",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-invalid-podtemplate",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						PodTemplateSpec: &runtime.RawExtension{
							Raw: []byte(`{"spec": {"containers": "invalid-not-an-array"}}`),
						},
					},
				},
			},
			FinalState: FinalState{
				Status: &mcpv1alpha1.EmbeddingServerStatus{
					Phase: mcpv1alpha1.EmbeddingServerPhaseFailed,
					Conditions: []metav1.Condition{{
						Type:   mcpv1alpha1.ConditionPodTemplateValid,
						Status: metav1.ConditionFalse,
						Reason: mcpv1alpha1.ConditionReasonPodTemplateInvalid,
					}},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with valid PodTemplateSpec (nodeSelector)",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-valid-podtemplate",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						PodTemplateSpec: &runtime.RawExtension{
							Raw: []byte(`{"spec":{"nodeSelector":{"disktype":"ssd"}}}`),
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								NodeSelector: map[string]string{"disktype": "ssd"},
							},
						},
					},
				},
				Status: &mcpv1alpha1.EmbeddingServerStatus{
					Conditions: []metav1.Condition{{
						Type:   mcpv1alpha1.ConditionPodTemplateValid,
						Status: metav1.ConditionTrue,
					}},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with HuggingFace token secret",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-hf-token",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						HFTokenSecretRef: &mcpv1alpha1.SecretKeyRef{
							Name: "hf-token-secret",
							Key:  "token",
						},
					},
				},
				Secrets: []*corev1.Secret{{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "hf-token-secret",
						Namespace: defaultNamespace,
					},
					Data: map[string][]byte{"token": []byte("hf_test_token_value")},
				}},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "embedding",
									Env: []corev1.EnvVar{{
										Name: "HF_TOKEN",
										ValueFrom: &corev1.EnvVarSource{
											SecretKeyRef: &corev1.SecretKeySelector{
												LocalObjectReference: corev1.LocalObjectReference{Name: "hf-token-secret"},
												Key:                  "token",
											},
										},
									}},
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with custom environment variables",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-custom-env",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						Env: []mcpv1alpha1.EnvVar{
							{Name: "CUSTOM_VAR_1", Value: "value1"},
							{Name: "CUSTOM_VAR_2", Value: "value2"},
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "embedding",
									Env: []corev1.EnvVar{
										{Name: "CUSTOM_VAR_1", Value: "value1"},
										{Name: "CUSTOM_VAR_2", Value: "value2"},
									},
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with custom args",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-custom-args",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
						Args:  []string{"--max-concurrent-requests", "512", "--tokenization-workers", "4"},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "embedding",
									Args: []string{"--model-id", "sentence-transformers/all-MiniLM-L6-v2", "--max-concurrent-requests", "512", "--tokenization-workers", "4"},
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with custom port",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-custom-port",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  9090,
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "embedding",
									Args: []string{"--port", "9090"},
								}},
							},
						},
					},
				},
				Service: &corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 9090}}}},
				Status:  &mcpv1alpha1.EmbeddingServerStatus{URL: "http://test-custom-port.default.svc.cluster.local:9090"},
			},
		},
		{
			Name: "When creating an EmbeddingServer with ImagePullPolicy Always",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-imagepullpolicy-always",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model:           "sentence-transformers/all-MiniLM-L6-v2",
						Image:           "ghcr.io/huggingface/text-embeddings-inference:latest",
						ImagePullPolicy: "Always",
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:            "embedding",
									ImagePullPolicy: corev1.PullAlways,
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with ImagePullPolicy Never",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-imagepullpolicy-never",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model:           "sentence-transformers/all-MiniLM-L6-v2",
						Image:           "ghcr.io/huggingface/text-embeddings-inference:latest",
						ImagePullPolicy: "Never",
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:            "embedding",
									ImagePullPolicy: corev1.PullNever,
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with model cache and custom storage class",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cache-storageclass",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						ModelCache: &mcpv1alpha1.ModelCacheConfig{
							Enabled:          true,
							Size:             "50Gi",
							StorageClassName: ptr.To("fast-ssd"),
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
							ObjectMeta: metav1.ObjectMeta{Name: "model-cache"},
							Spec: corev1.PersistentVolumeClaimSpec{
								StorageClassName: ptr.To("fast-ssd"),
								AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
								Resources: corev1.VolumeResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("50Gi")},
								},
							},
						}},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with model cache ReadWriteMany access mode",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cache-rwx",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						ModelCache: &mcpv1alpha1.ModelCacheConfig{
							Enabled:    true,
							Size:       "10Gi",
							AccessMode: "ReadWriteMany",
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						VolumeClaimTemplates: []corev1.PersistentVolumeClaim{{
							ObjectMeta: metav1.ObjectMeta{Name: "model-cache"},
							Spec: corev1.PersistentVolumeClaimSpec{
								AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
							},
						}},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with PodTemplateSpec tolerations",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-tolerations",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						PodTemplateSpec: &runtime.RawExtension{
							Raw: []byte(`{"spec":{"tolerations":[{"key":"gpu","operator":"Exists","effect":"NoSchedule"}]}}`),
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Tolerations: []corev1.Toleration{{
									Key:      "gpu",
									Operator: corev1.TolerationOpExists,
									Effect:   corev1.TaintEffectNoSchedule,
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with PodTemplateSpec serviceAccountName",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-serviceaccount",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						PodTemplateSpec: &runtime.RawExtension{
							Raw: []byte(`{"spec":{"serviceAccountName":"custom-sa"}}`),
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Replicas: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								ServiceAccountName: "custom-sa",
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with ResourceOverrides on StatefulSet",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-resource-overrides-sts",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						ResourceOverrides: &mcpv1alpha1.EmbeddingResourceOverrides{
							StatefulSet: &mcpv1alpha1.EmbeddingStatefulSetOverrides{
								ResourceMetadataOverrides: mcpv1alpha1.ResourceMetadataOverrides{
									Annotations: map[string]string{"custom-annotation": "sts-value"},
									Labels:      map[string]string{"custom-label": "sts-value"},
								},
							},
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/name":       "embeddingserver",
							"app.kubernetes.io/instance":   "test-resource-overrides-sts",
							"app.kubernetes.io/component":  "embedding-server",
							"app.kubernetes.io/managed-by": "toolhive-operator",
							"custom-label":                 "sts-value",
						},
						Annotations: map[string]string{
							"custom-annotation": "sts-value",
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer with ResourceOverrides on Service",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-resource-overrides-svc",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						ResourceOverrides: &mcpv1alpha1.EmbeddingResourceOverrides{
							Service: &mcpv1alpha1.ResourceMetadataOverrides{
								Annotations: map[string]string{"service-annotation": "svc-value"},
								Labels:      map[string]string{"service-label": "svc-value"},
							},
						},
					},
				},
			},
			FinalState: FinalState{
				Service: &corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app.kubernetes.io/name":       "embeddingserver",
							"app.kubernetes.io/instance":   "test-resource-overrides-svc",
							"app.kubernetes.io/component":  "embedding-server",
							"app.kubernetes.io/managed-by": "toolhive-operator",
							"service-label":                "svc-value",
						},
						Annotations: map[string]string{
							"service-annotation": "svc-value",
						},
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{{Port: 8080}},
					},
				},
			},
		},
		// TODO(embeddingserver): Update assertion when ResourceOverrides on pod template is implemented.
		// Expected: Annotations: {"pod-annotation": "pod-value"}, Labels: {"pod-label": "pod-value"} on pod template
		{
			Name: "When creating an EmbeddingServer with ResourceOverrides on pod template",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-resource-overrides-pod",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						ResourceOverrides: &mcpv1alpha1.EmbeddingResourceOverrides{
							StatefulSet: &mcpv1alpha1.EmbeddingStatefulSetOverrides{
								PodTemplateMetadataOverrides: &mcpv1alpha1.ResourceMetadataOverrides{
									Annotations: map[string]string{"pod-annotation": "pod-value"},
									Labels:      map[string]string{"pod-label": "pod-value"},
								},
							},
						},
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Replicas: ptr.To(int32(1)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{
									"app.kubernetes.io/name":     "embeddingserver",
									"app.kubernetes.io/instance": "test-resource-overrides-pod",
									"pod-label":                  "pod-value",
								},
								Annotations: map[string]string{
									"pod-annotation": "pod-value",
								},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer verifies container port",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-container-port",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
						Port:  8080,
					},
				},
			},
			FinalState: FinalState{
				StatefulSet: &appsv1.StatefulSet{
					Spec: appsv1.StatefulSetSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name: "embedding",
									Ports: []corev1.ContainerPort{{
										Name:          "http",
										ContainerPort: 8080,
										Protocol:      corev1.ProtocolTCP,
									}},
								}},
							},
						},
					},
				},
			},
		},
		{
			Name: "When creating an EmbeddingServer verifies Service selector and type",
			InitialState: InitialState{
				EmbeddingServer: &mcpv1alpha1.EmbeddingServer{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-service-selector",
						Namespace: defaultNamespace,
					},
					Spec: mcpv1alpha1.EmbeddingServerSpec{
						Model: "sentence-transformers/all-MiniLM-L6-v2",
						Image: "ghcr.io/huggingface/text-embeddings-inference:latest",
					},
				},
			},
			FinalState: FinalState{
				Service: &corev1.Service{
					Spec: corev1.ServiceSpec{
						Type: corev1.ServiceTypeClusterIP,
						Selector: map[string]string{
							"app.kubernetes.io/name":     "embeddingserver",
							"app.kubernetes.io/instance": "test-service-selector",
						},
						Ports: []corev1.ServicePort{{Port: 8080}},
					},
				},
			},
		},
	}

	// Run all test cases
	for _, tc := range testCases {
		runTestCase(tc)
	}
})

// --- Equality helper functions for K8s objects ---
// These functions accept an optional Gomega parameter for use inside Eventually blocks.
// When g is nil, they use the global Expect.

// verifyStatefulSetEquals checks that actual StatefulSet contains expected fields.
func verifyStatefulSetEquals(actual, expected *appsv1.StatefulSet) {
	verifyStatefulSetEqualsG(Default, actual, expected)
}

// verifyStatefulSetEqualsG is the Gomega-aware version for use in Eventually blocks.
func verifyStatefulSetEqualsG(g Gomega, actual, expected *appsv1.StatefulSet) {
	// Replicas
	if expected.Spec.Replicas != nil {
		g.Expect(actual.Spec.Replicas).To(Equal(expected.Spec.Replicas), "replicas mismatch")
	}

	// Labels
	for k, v := range expected.Labels {
		g.Expect(actual.Labels).To(HaveKeyWithValue(k, v))
	}

	// Annotations
	for k, v := range expected.Annotations {
		g.Expect(actual.Annotations).To(HaveKeyWithValue(k, v))
	}

	// NodeSelector
	for k, v := range expected.Spec.Template.Spec.NodeSelector {
		g.Expect(actual.Spec.Template.Spec.NodeSelector).To(HaveKeyWithValue(k, v))
	}

	// Tolerations
	for _, exp := range expected.Spec.Template.Spec.Tolerations {
		g.Expect(actual.Spec.Template.Spec.Tolerations).To(ContainElement(exp))
	}

	// ServiceAccountName
	if expected.Spec.Template.Spec.ServiceAccountName != "" {
		g.Expect(actual.Spec.Template.Spec.ServiceAccountName).To(Equal(expected.Spec.Template.Spec.ServiceAccountName))
	}

	// Pod template labels
	for k, v := range expected.Spec.Template.Labels {
		g.Expect(actual.Spec.Template.Labels).To(HaveKeyWithValue(k, v))
	}

	// Pod template annotations
	for k, v := range expected.Spec.Template.Annotations {
		g.Expect(actual.Spec.Template.Annotations).To(HaveKeyWithValue(k, v))
	}

	// Containers
	for i, exp := range expected.Spec.Template.Spec.Containers {
		verifyContainerEqualsG(g, actual.Spec.Template.Spec.Containers[i], exp)
	}

	// VolumeClaimTemplates
	for i, exp := range expected.Spec.VolumeClaimTemplates {
		verifyPVCEqualsG(g, actual.Spec.VolumeClaimTemplates[i], exp)
	}
}

// verifyContainerEqualsG is the Gomega-aware version for use in Eventually blocks.
func verifyContainerEqualsG(g Gomega, actual, expected corev1.Container) {
	if expected.Name != "" {
		g.Expect(actual.Name).To(Equal(expected.Name))
	}
	if expected.Image != "" {
		g.Expect(actual.Image).To(Equal(expected.Image))
	}
	if expected.ImagePullPolicy != "" {
		g.Expect(actual.ImagePullPolicy).To(Equal(expected.ImagePullPolicy))
	}

	for _, arg := range expected.Args {
		g.Expect(actual.Args).To(ContainElement(arg))
	}

	for _, env := range expected.Env {
		g.Expect(actual.Env).To(ContainElement(HaveField("Name", env.Name)))
	}

	for _, vm := range expected.VolumeMounts {
		g.Expect(actual.VolumeMounts).To(ContainElement(And(
			HaveField("Name", vm.Name),
			HaveField("MountPath", vm.MountPath),
		)))
	}

	// Check resource limits - only verify if expected has values
	for k, v := range expected.Resources.Limits {
		g.Expect(actual.Resources.Limits[k]).To(Equal(v))
	}

	// Check resource requests - only verify if expected has values
	for k, v := range expected.Resources.Requests {
		g.Expect(actual.Resources.Requests[k]).To(Equal(v))
	}

	if expected.LivenessProbe != nil {
		g.Expect(actual.LivenessProbe).NotTo(BeNil())
	}
	if expected.ReadinessProbe != nil {
		g.Expect(actual.ReadinessProbe).NotTo(BeNil())
	}

	// Container ports
	for _, exp := range expected.Ports {
		g.Expect(actual.Ports).To(ContainElement(And(
			HaveField("Name", exp.Name),
			HaveField("ContainerPort", exp.ContainerPort),
			HaveField("Protocol", exp.Protocol),
		)))
	}
}

// verifyPVCEqualsG is the Gomega-aware version for use in Eventually blocks.
func verifyPVCEqualsG(g Gomega, actual, expected corev1.PersistentVolumeClaim) {
	if expected.Name != "" {
		g.Expect(actual.Name).To(Equal(expected.Name))
	}
	for _, mode := range expected.Spec.AccessModes {
		g.Expect(actual.Spec.AccessModes).To(ContainElement(mode))
	}
	// StorageClassName
	if expected.Spec.StorageClassName != nil {
		g.Expect(actual.Spec.StorageClassName).To(Equal(expected.Spec.StorageClassName))
	}
	// Storage size
	if expected.Spec.Resources.Requests != nil {
		expectedSize := expected.Spec.Resources.Requests[corev1.ResourceStorage]
		actualSize := actual.Spec.Resources.Requests[corev1.ResourceStorage]
		g.Expect(actualSize.Cmp(expectedSize)).To(Equal(0), "storage size mismatch")
	}
}

// verifyServiceEquals checks that actual Service contains expected ports.
func verifyServiceEquals(actual, expected *corev1.Service) {
	verifyServiceEqualsG(Default, actual, expected)
}

// verifyServiceEqualsG is the Gomega-aware version for use in Eventually blocks.
func verifyServiceEqualsG(g Gomega, actual, expected *corev1.Service) {
	// Ports
	for i, exp := range expected.Spec.Ports {
		g.Expect(actual.Spec.Ports[i].Port).To(Equal(exp.Port))
	}

	// Service type
	if expected.Spec.Type != "" {
		g.Expect(actual.Spec.Type).To(Equal(expected.Spec.Type))
	}

	// Selector
	for k, v := range expected.Spec.Selector {
		g.Expect(actual.Spec.Selector).To(HaveKeyWithValue(k, v))
	}

	// Labels
	for k, v := range expected.Labels {
		g.Expect(actual.Labels).To(HaveKeyWithValue(k, v))
	}

	// Annotations
	for k, v := range expected.Annotations {
		g.Expect(actual.Annotations).To(HaveKeyWithValue(k, v))
	}
}

// verifyStatusEquals checks status fields match and finalizer is present.
func verifyStatusEquals(actual *mcpv1alpha1.EmbeddingServer, expected *mcpv1alpha1.EmbeddingServerStatus) bool {
	if expected != nil && expected.Phase != "" && actual.Status.Phase != expected.Phase {
		return false
	}
	if expected != nil && expected.URL != "" && actual.Status.URL != expected.URL {
		return false
	}
	// Always verify finalizer is present
	if !containsString(actual.Finalizers, "embeddingserver.toolhive.stacklok.dev/finalizer") {
		return false
	}
	return true
}

// containsString checks if a slice contains a string.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// verifyOwnerReference checks owner reference is set correctly.
func verifyOwnerReference(ownerRefs []metav1.OwnerReference, embedding *mcpv1alpha1.EmbeddingServer, _ string) {
	Expect(ownerRefs).To(HaveLen(1))
	Expect(ownerRefs[0].APIVersion).To(Equal("toolhive.stacklok.dev/v1alpha1"))
	Expect(ownerRefs[0].Kind).To(Equal("EmbeddingServer"))
	Expect(ownerRefs[0].Name).To(Equal(embedding.Name))
	Expect(ownerRefs[0].UID).To(Equal(embedding.UID))
	Expect(ownerRefs[0].Controller).To(HaveValue(BeTrue()))
	Expect(ownerRefs[0].BlockOwnerDeletion).To(HaveValue(BeTrue()))
}
