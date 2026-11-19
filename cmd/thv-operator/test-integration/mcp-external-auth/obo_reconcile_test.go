// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

// These tests drive the OBO reconcile-time validation through the real
// apiserver: a well-formed obo config becomes Valid=True, a malformed one
// becomes Valid=False / Reason=InvalidConfig, and an MCPServer that references
// an obo config without incoming OIDC is rejected with InvalidConfig.
var _ = Describe("MCPExternalAuthConfig OBO reconcile", Label("k8s", "obo"), func() {
	const (
		namespace = "default"
		timeout   = 30 * time.Second
		interval  = 250 * time.Millisecond
	)

	validOBO := func() *mcpv1beta1.OBOConfig {
		return &mcpv1beta1.OBOConfig{
			TenantID:        "contoso.onmicrosoft.com",
			ClientID:        "app-client-id",
			ClientSecretRef: &mcpv1beta1.SecretKeyRef{Name: "entra-client", Key: "clientSecret"},
			Scopes:          []string{"api://backend/.default"},
		}
	}

	createOBOConfig := func(name string, obo *mcpv1beta1.OBOConfig) {
		Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPExternalAuthConfig{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
				Type: mcpv1beta1.ExternalAuthTypeOBO,
				OBO:  obo,
			},
		})).To(Succeed())
	}

	validCondition := func(name string) func() *metav1.Condition {
		return func() *metav1.Condition {
			updated := &mcpv1beta1.MCPExternalAuthConfig{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, updated); err != nil {
				return nil
			}
			return meta.FindStatusCondition(updated.Status.Conditions, mcpv1beta1.ConditionTypeValid)
		}
	}

	It("reconciles a well-formed obo config to Valid=True", func() {
		createOBOConfig("obo-reconcile-valid", validOBO())

		Eventually(validCondition("obo-reconcile-valid"), timeout, interval).Should(
			And(Not(BeNil()),
				HaveField("Status", metav1.ConditionTrue),
				HaveField("Reason", "ValidationSucceeded")))
	})

	DescribeTable("reconciles a malformed obo config to Valid=False / InvalidConfig",
		func(name string, mutate func(*mcpv1beta1.OBOConfig), wantMessage string) {
			obo := validOBO()
			mutate(obo)
			createOBOConfig(name, obo)

			Eventually(validCondition(name), timeout, interval).Should(And(
				Not(BeNil()),
				HaveField("Status", metav1.ConditionFalse),
				HaveField("Reason", mcpv1beta1.ConditionReasonInvalidConfig),
				HaveField("Message", wantMessage),
			))
		},
		Entry("missing tenant", "obo-reconcile-no-tenant",
			func(c *mcpv1beta1.OBOConfig) { c.TenantID = "" }, "entra obo: TenantID is required"),
		Entry("missing client id", "obo-reconcile-no-client",
			func(c *mcpv1beta1.OBOConfig) { c.ClientID = "" }, "obo: clientId is required"),
		Entry("missing client secret ref", "obo-reconcile-no-secret",
			func(c *mcpv1beta1.OBOConfig) { c.ClientSecretRef = nil }, "obo: clientSecretRef must set both name and key"),
		Entry("neither audience nor scopes", "obo-reconcile-no-target",
			func(c *mcpv1beta1.OBOConfig) { c.Scopes = nil }, "obo: at least one of audience or scopes is required"),
	)

	It("rejects an MCPServer that references an obo config without oidcConfigRef", func() {
		createOBOConfig("obo-reconcile-server-ref", validOBO())

		Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: "obo-no-oidc-server", Namespace: namespace},
			Spec: mcpv1beta1.MCPServerSpec{
				Image:                 "ghcr.io/example/mcp:latest",
				ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: "obo-reconcile-server-ref"},
			},
		})).To(Succeed())

		Eventually(func() *metav1.Condition {
			server := &mcpv1beta1.MCPServer{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "obo-no-oidc-server", Namespace: namespace}, server); err != nil {
				return nil
			}
			return meta.FindStatusCondition(server.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
		}, timeout, interval).Should(And(
			Not(BeNil()),
			HaveField("Status", metav1.ConditionFalse),
			HaveField("Reason", mcpv1beta1.ConditionReasonInvalidConfig),
			HaveField("Message", ctrlutil.MsgOBORequiresIncomingAuth),
		))
	})
})
