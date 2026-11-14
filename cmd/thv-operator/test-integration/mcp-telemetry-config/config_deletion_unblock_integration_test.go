// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
)

// These cover the authz and webhook config controllers, which have no suite of
// their own: deletion blocked by a referencing MCPServer must clear on the
// workload watch, well inside the 30s requeue. Releasing by spec update (rather
// than deleting the server) exercises the watch's old-object mapping.
var _ = DescribeTable("Config deletion unblocks when the MCPServer reference goes away",
	func(cfg client.Object, finalizer string, setRef func(*mcpv1beta1.MCPServerSpec), dropRefByUpdate bool) {
		key := client.ObjectKeyFromObject(cfg)
		Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, key, cfg)).To(Succeed())
			g.Expect(controllerutil.ContainsFinalizer(cfg, finalizer)).To(BeTrue())
		}, timeout, interval).Should(Succeed())

		server := &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: cfg.GetName() + "-server", Namespace: "default"},
			Spec:       mcpv1beta1.MCPServerSpec{Image: "example/mcp-server:latest"},
		}
		setRef(&server.Spec)
		Expect(k8sClient.Create(ctx, server)).To(Succeed())
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKeyFromObject(server), &mcpv1beta1.MCPServer{})
		}, timeout, interval).Should(Succeed())

		Expect(k8sClient.Delete(ctx, cfg)).To(Succeed())
		Consistently(func() error {
			return k8sClient.Get(ctx, key, cfg)
		}, 3*time.Second, interval).Should(Succeed(), "config should stay while referenced")

		if dropRefByUpdate {
			DeferCleanup(k8sClient.Delete, ctx, server)
			Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(server), server)).To(Succeed())
			server.Spec = mcpv1beta1.MCPServerSpec{Image: server.Spec.Image}
			Expect(k8sClient.Update(ctx, server)).To(Succeed())
		} else {
			Expect(k8sClient.Delete(ctx, server)).To(Succeed())
		}
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, key, cfg))
		}, deletionUnblockTimeout, interval).Should(BeTrue(), "config should be deleted once unreferenced")
	},
	Entry("MCPAuthzConfig, server deleted",
		authzConfig("authz-deletion-unblock"),
		controllers.AuthzConfigFinalizerName,
		authzRef("authz-deletion-unblock"),
		false,
	),
	Entry("MCPAuthzConfig, reference dropped by spec update",
		authzConfig("authz-ref-dropped"),
		controllers.AuthzConfigFinalizerName,
		authzRef("authz-ref-dropped"),
		true,
	),
	Entry("MCPWebhookConfig, server deleted",
		&mcpv1alpha1.MCPWebhookConfig{
			ObjectMeta: metav1.ObjectMeta{Name: "webhook-deletion-unblock", Namespace: "default"},
			Spec: mcpv1beta1.MCPWebhookConfigSpec{
				Validating: []mcpv1beta1.WebhookSpec{{Name: "v", URL: "https://example.com/validate"}},
			},
		},
		controllers.WebhookConfigFinalizerName,
		func(s *mcpv1beta1.MCPServerSpec) {
			s.WebhookConfigRef = &mcpv1beta1.WebhookConfigRef{Name: "webhook-deletion-unblock"}
		},
		false,
	),
)

func authzConfig(name string) *mcpv1beta1.MCPAuthzConfig {
	return &mcpv1beta1.MCPAuthzConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: mcpv1beta1.MCPAuthzConfigSpec{
			Type: "cedarv1",
			Config: runtime.RawExtension{
				Raw: []byte(`{"policies":["permit(principal, action, resource);"],"entities_json":"[]"}`),
			},
		},
	}
}

func authzRef(name string) func(*mcpv1beta1.MCPServerSpec) {
	return func(s *mcpv1beta1.MCPServerSpec) {
		s.AuthzConfigRef = &mcpv1beta1.MCPAuthzConfigReference{Name: name}
	}
}
