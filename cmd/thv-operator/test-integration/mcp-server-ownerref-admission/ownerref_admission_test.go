// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ownerrefadmission

import (
	"fmt"
	"slices"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// On OpenShift the OwnerReferencesPermissionEnforcement admission plugin
// rejected the proxy-runner creating its Services, because they are owned by
// the StatefulSet with blockOwnerDeletion: true and the proxy-runner Role
// lacked "update" on apps/statefulsets/finalizers (#6835, fixed in #6756).
//
// This test takes the Role the operator really generates for an MCPServer and
// has the proxy-runner ServiceAccount create such a Service. The negative
// control uses the same Role minus the finalizers rule and must be rejected,
// which proves the plugin is active in this API server and that the positive
// case cannot pass vacuously.
var _ = Describe("Proxy-runner RBAC under OwnerReferencesPermissionEnforcement", Ordered, func() {
	const (
		timeout  = 30 * time.Second
		interval = 250 * time.Millisecond

		finalizersResource = "statefulsets/finalizers"
	)

	var (
		namespace     string
		mcpServerName string
		sts           *appsv1.StatefulSet
		runnerRole    *rbacv1.Role
	)

	// newService builds a Service shaped like the ones the proxy-runner creates
	// (pkg/container/kubernetes applyService): owned by the StatefulSet with
	// blockOwnerDeletion and controller both set.
	newService := func(name string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
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
				ClusterIP: corev1.ClusterIPNone,
				Selector:  map[string]string{"app": mcpServerName},
				Ports:     []corev1.ServicePort{{Name: "http", Port: 8080}},
			},
		}
	}

	// clientAs returns a client that acts as the given ServiceAccount, so RBAC
	// and admission see that identity rather than the envtest admin.
	clientAs := func(serviceAccount string) client.Client {
		impersonated := rest.CopyConfig(cfg)
		impersonated.Impersonate = rest.ImpersonationConfig{
			UserName: fmt.Sprintf("system:serviceaccount:%s:%s", namespace, serviceAccount),
		}
		c, err := client.New(impersonated, client.Options{Scheme: scheme.Scheme})
		Expect(err).NotTo(HaveOccurred())
		return c
	}

	BeforeAll(func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-ownerref-admission-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		namespace = ns.Name
		mcpServerName = "ownerref-admission"

		// SSE creates the Services; stdio does not.
		Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: namespace},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:     "example/mcp-server:latest",
				Transport: "sse",
				ProxyPort: 8080,
			},
		})).To(Succeed())

		runnerSA := ctrlutil.ProxyRunnerServiceAccountName(mcpServerName)
		runnerRole = &rbacv1.Role{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerSA, Namespace: namespace}, runnerRole)
		}, timeout, interval).Should(Succeed())
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: runnerSA, Namespace: namespace}, &rbacv1.RoleBinding{})
		}, timeout, interval).Should(Succeed())

		// The StatefulSet owner is created by the admin: it stands in for the
		// workload the proxy-runner deploys.
		sts = &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: mcpServerName, Namespace: namespace},
			Spec: appsv1.StatefulSetSpec{
				Replicas:    ptr.To(int32(0)),
				ServiceName: "mcp-" + mcpServerName + "-headless",
				Selector:    &metav1.LabelSelector{MatchLabels: map[string]string{"app": mcpServerName}},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": mcpServerName}},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "mcp", Image: "example/mcp-server:latest"}}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sts)).To(Succeed())
	})

	AfterAll(func() {
		Expect(k8sClient.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}})).To(Succeed())
	})

	It("lets the operator-generated proxy-runner Role create a Service owned with blockOwnerDeletion", func() {
		runner := clientAs(ctrlutil.ProxyRunnerServiceAccountName(mcpServerName))

		// RBAC objects reach the API server's authorizer through an informer, so
		// the first attempts can be denied before the Role is visible to it.
		Eventually(func() error {
			return runner.Create(ctx, newService("mcp-"+mcpServerName+"-headless"))
		}, timeout, interval).Should(Succeed())
	})

	It("rejects the same Service when the Role lacks update on statefulsets/finalizers", func() {
		const noFinalizersSA = "ownerref-admission-no-finalizers"

		// Derive the control Role from the operator's real output so it differs
		// only by the one rule under test.
		var rules []rbacv1.PolicyRule
		for _, rule := range runnerRole.Rules {
			if !slices.Contains(rule.Resources, finalizersResource) {
				rules = append(rules, rule)
			}
		}
		Expect(rules).To(HaveLen(len(runnerRole.Rules)-1),
			"the operator Role should contain exactly one %s rule", finalizersResource)

		Expect(k8sClient.Create(ctx, &corev1.ServiceAccount{
			ObjectMeta: metav1.ObjectMeta{Name: noFinalizersSA, Namespace: namespace},
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: noFinalizersSA, Namespace: namespace},
			Rules:      rules,
		})).To(Succeed())
		Expect(k8sClient.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: noFinalizersSA, Namespace: namespace},
			RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: noFinalizersSA},
			Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: noFinalizersSA, Namespace: namespace}},
		})).To(Succeed())

		runner := clientAs(noFinalizersSA)

		// Until the Role is visible to the authorizer the create is denied for a
		// different reason, so wait for the plugin's own message.
		Eventually(func() string {
			err := runner.Create(ctx, newService("mcp-"+mcpServerName))
			if err == nil {
				return "created"
			}
			Expect(apierrors.IsForbidden(err)).To(BeTrue(), "expected Forbidden, got: %v", err)
			return err.Error()
		}, timeout, interval).Should(ContainSubstring("cannot set blockOwnerDeletion"))
	})
})
