// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ownerrefadmission

import (
	"context"
	"fmt"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	k8sruntime "github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// On OpenShift the OwnerReferencesPermissionEnforcement admission plugin
// rejects an ownerReference with blockOwnerDeletion: true unless the caller
// may "update" the owner's finalizers subresource. The proxy-runner used to set
// blockOwnerDeletion on the Services it owns by its StatefulSet, so it needed
// "update" on apps/statefulsets/finalizers (#6835). #6836 removed the flag
// instead of granting the verb: it only affects foreground cascading deletion,
// which ToolHive never uses.
//
// This test proves both halves against an API server with the plugin enabled:
//
//   - The runner's real code path (pkg/container/kubernetes Client.DeployWorkload),
//     running as the proxy-runner ServiceAccount with the Role the operator really
//     generates, creates both Services. If the runner sets blockOwnerDeletion
//     again, the create is rejected and this case fails.
//   - Negative control: the same ServiceAccount creating a Service owned by the
//     StatefulSet with blockOwnerDeletion: true is rejected with the plugin's
//     message. That proves the plugin is active (so the positive case cannot
//     pass vacuously) and pins that the Role has no statefulsets/finalizers
//     grant. If the grant is re-added, this case fails.
var _ = Describe("Proxy-runner under OwnerReferencesPermissionEnforcement", Ordered, func() {
	const (
		timeout  = 30 * time.Second
		interval = 250 * time.Millisecond
	)

	var (
		namespace     string
		mcpServerName string
		runnerSA      string
		// headlessName and mcpName are the Service names the runner creates.
		headlessName string
		mcpName      string
	)

	// impersonate returns a copy of the envtest config that acts as the given
	// ServiceAccount, so RBAC and admission see that identity, not the admin.
	impersonate := func(serviceAccount string) *rest.Config {
		impersonated := rest.CopyConfig(cfg)
		impersonated.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount),
		}
		return impersonated
	}

	// waitForRBAC blocks until the API server's authorizer lets the
	// ServiceAccount perform the verb. The operator creates the Role and
	// RoleBinding asynchronously and the authorizer learns about them through an
	// informer, so until then every request is denied for a reason unrelated to
	// the admission plugin under test.
	waitForRBAC := func(serviceAccount, group, resource string) {
		admin, err := kubernetes.NewForConfig(cfg)
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() bool {
			review, err := admin.AuthorizationV1().SubjectAccessReviews().Create(ctx,
				&authorizationv1.SubjectAccessReview{Spec: authorizationv1.SubjectAccessReviewSpec{
					User: fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount),
					ResourceAttributes: &authorizationv1.ResourceAttributes{
						Namespace: namespace, Verb: "create", Group: group, Resource: resource,
					},
				}}, metav1.CreateOptions{})
			return err == nil && review.Status.Allowed
		}, timeout, interval).Should(BeTrue(), "%s should be allowed to create %s", serviceAccount, resource)
	}

	// markStatefulSetReady plays the StatefulSet controller, which envtest does
	// not run: DeployWorkload blocks until the StatefulSet reports ready, and it
	// does so only after it has created the Services. It returns when ctx ends.
	markStatefulSetReady := func(ctx context.Context) {
		defer GinkgoRecover()
		for ctx.Err() == nil {
			current := &appsv1.StatefulSet{}
			key := types.NamespacedName{Name: mcpServerName, Namespace: namespace}
			if err := k8sClient.Get(ctx, key, current); err == nil && current.Status.ReadyReplicas == 0 {
				current.Status.ObservedGeneration = current.Generation
				current.Status.Replicas = ptr.Deref(current.Spec.Replicas, 1)
				current.Status.UpdatedReplicas = current.Status.Replicas
				current.Status.ReadyReplicas = current.Status.Replicas
				_ = k8sClient.Status().Update(ctx, current) // conflicts retry on the next tick
			}
			select {
			case <-ctx.Done():
			case <-time.After(interval):
			}
		}
	}

	BeforeAll(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-ownerref-admission-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name
		mcpServerName = "ownerref-admission"
		runnerSA = ctrlutil.ProxyRunnerServiceAccountName(mcpServerName)
		headlessName = "mcp-" + mcpServerName + "-headless"
		mcpName = "mcp-" + mcpServerName

		// The runner learns its namespace from POD_NAMESPACE, which the operator
		// sets on the proxy-runner Deployment.
		previous, hadPrevious := os.LookupEnv("POD_NAMESPACE")
		Expect(os.Setenv("POD_NAMESPACE", namespace)).To(Succeed())
		DeferCleanup(func() {
			if hadPrevious {
				Expect(os.Setenv("POD_NAMESPACE", previous)).To(Succeed())
			} else {
				Expect(os.Unsetenv("POD_NAMESPACE")).To(Succeed())
			}
		})

		// SSE is a transport that makes the runner create the Services; stdio
		// does not. The MCPServer makes the operator generate the proxy-runner
		// ServiceAccount, Role and RoleBinding under test.
		Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: namespace},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:     "example/mcp-server:latest",
				Transport: "sse",
				ProxyPort: 8080,
			},
		})).To(Succeed())

		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerSA, Namespace: namespace}, &corev1.ServiceAccount{})
		}, timeout, interval).Should(Succeed())
		waitForRBAC(runnerSA, "apps", "statefulsets")
		waitForRBAC(runnerSA, "", "services")
	})

	AfterAll(func() {
		Expect(k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("lets the runner create its Services without blockOwnerDeletion or a finalizers grant", func() {
		clientset, err := kubernetes.NewForConfig(impersonate(runnerSA))
		Expect(err).NotTo(HaveOccurred())
		runner := k8sruntime.NewClientWithConfig(clientset, impersonate(runnerSA))

		options := runtime.NewDeployWorkloadOptions()
		options.PortBindings = map[string][]runtime.PortBinding{"8080/tcp": {{HostPort: "8080"}}}

		deployCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		DeferCleanup(cancel)
		go markStatefulSetReady(deployCtx)

		_, err = runner.DeployWorkload(
			deployCtx,
			"example/mcp-server:latest",
			mcpServerName,
			nil,
			nil,
			map[string]string{},
			nil,
			"sse",
			options,
			false,
		)
		Expect(err).NotTo(HaveOccurred())

		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: mcpServerName, Namespace: namespace}, sts)).To(Succeed())

		for _, name := range []string{headlessName, mcpName} {
			svc := &corev1.Service{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, svc)).To(Succeed())
			Expect(svc.OwnerReferences).To(HaveLen(1), "service %s", name)
			owner := svc.OwnerReferences[0]
			Expect(owner.Kind).To(Equal("StatefulSet"), "service %s", name)
			Expect(owner.Name).To(Equal(sts.Name), "service %s", name)
			Expect(owner.UID).To(Equal(sts.UID), "service %s", name)
			Expect(owner.Controller).To(Equal(ptr.To(true)), "service %s", name)
			Expect(owner.BlockOwnerDeletion).To(BeNil(), "service %s", name)
		}
	})

	It("rejects a Service owned by the StatefulSet with blockOwnerDeletion for the same Role", func() {
		sts := &appsv1.StatefulSet{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: mcpServerName, Namespace: namespace}, sts)).To(Succeed())

		impersonated, err := client.New(impersonate(runnerSA), client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())

		err = impersonated.Create(ctx, &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mcpName + "-control",
				Namespace: namespace,
				OwnerReferences: []metav1.OwnerReference{{
					APIVersion:         appsv1.SchemeGroupVersion.String(),
					Kind:               "StatefulSet",
					Name:               sts.Name,
					UID:                sts.UID,
					BlockOwnerDeletion: ptr.To(true),
					Controller:         ptr.To(true),
				}},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{"app": mcpServerName},
				Ports:    []corev1.ServicePort{{Name: "http", Port: 8080}},
			},
		})
		Expect(apierrors.IsForbidden(err)).To(BeTrue(), "expected Forbidden, got: %v", err)
		// RBAC already allows creating Services (waited for in BeforeAll), so
		// this Forbidden comes from the admission plugin.
		Expect(err.Error()).To(ContainSubstring("cannot set blockOwnerDeletion"))
	})
})
