// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// wakeupTimeout is deliberately well below the 30s fallback requeue in
// handleDeletion. A blocked deletion that clears within this window could only
// have been woken by the referrer's ref-change watch event, so the assertion
// fails if that event wiring regresses even though the fallback would still
// eventually reclaim the object.
const wakeupTimeout = 10 * time.Second

var _ = Describe("MCPOIDCConfig deletion wakeup on referrer ref changes", func() {
	// Each referrer type references an MCPOIDCConfig through a different spec
	// path, so repointing that reference exercises a distinct watch + predicate +
	// mapper wiring. The shared body proves all three free a pending deletion via
	// the event rather than the fallback.
	DescribeTable("frees a blocked deletion via the ref-change event, not the 30s fallback",
		func(createReferrer func(ns, cfgName string) string, repoint func(ns, name, cfgName string)) {
			ns := newIsolatedNamespace("test-oidc-wakeup-")
			const primaryCfg = "wakeup-oidc-primary"
			const otherCfg = "wakeup-oidc-other"

			// primaryCfg is the config under deletion; otherCfg is a valid repoint
			// target so the referrer stays schema-valid after the reference moves.
			createReadyOIDCConfig(ns, primaryCfg)
			createReadyOIDCConfig(ns, otherCfg)

			name := createReferrer(ns, primaryCfg)

			// Deleting the config must not remove it while the referrer points at
			// it: the finalizer holds it in a Terminating state with DeletionBlocked.
			primary := &mcpv1beta1.MCPOIDCConfig{
				ObjectMeta: metav1.ObjectMeta{Name: primaryCfg, Namespace: ns},
			}
			Expect(k8sClient.Delete(ctx, primary)).To(Succeed())
			Eventually(func() bool {
				got := &mcpv1beta1.MCPOIDCConfig{}
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: primaryCfg, Namespace: ns}, got); err != nil {
					return false
				}
				if got.DeletionTimestamp.IsZero() {
					return false
				}
				cond := meta.FindStatusCondition(got.Status.Conditions, mcpv1beta1.ConditionTypeDeletionBlocked)
				return cond != nil && cond.Status == metav1.ConditionTrue
			}, timeout, interval).Should(BeTrue(),
				"deletion should stay blocked while the referrer still points at the config")

			// Repoint the referrer at otherCfg. The resulting ref-change event is
			// the only wakeup available inside wakeupTimeout — the handleDeletion
			// requeue is 30s out — so a deletion this fast proves the event drove it.
			repoint(ns, name, otherCfg)

			Eventually(func() bool {
				got := &mcpv1beta1.MCPOIDCConfig{}
				err := k8sClient.Get(ctx, types.NamespacedName{Name: primaryCfg, Namespace: ns}, got)
				return errors.IsNotFound(err)
			}, wakeupTimeout, interval).Should(BeTrue(),
				"the ref-change event should remove the finalizer well before the 30s fallback requeue")
		},
		Entry("MCPServer referrer", createMCPServerReferrer, repointMCPServer),
		Entry("VirtualMCPServer referrer", createVMCPReferrer, repointVMCP),
		Entry("MCPRemoteProxy referrer", createRemoteProxyReferrer, repointRemoteProxy),
	)
})

// newIsolatedNamespace creates a uniquely-named namespace so each table entry
// runs against its own set of configs and referrers.
func newIsolatedNamespace(prefix string) string {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: prefix}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	return ns.Name
}

// createReadyOIDCConfig creates an inline MCPOIDCConfig and waits until it has
// reconciled at least once (ConfigHash set), which also guarantees the finalizer
// is in place so a subsequent deletion is blockable.
func createReadyOIDCConfig(ns, name string) {
	cfg := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type: mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{
				Issuer:   "https://accounts.google.com",
				ClientID: "test-client",
			},
		},
	}
	Expect(k8sClient.Create(ctx, cfg)).To(Succeed())
	Eventually(func() bool {
		got := &mcpv1beta1.MCPOIDCConfig{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return false
		}
		return got.Status.ConfigHash != ""
	}, timeout, interval).Should(BeTrue())
}

// expectRefValidated waits until the referrer's OIDCConfigRefValidated condition
// is True, confirming the controller observed the reference (and the reverse
// index carries it) before the test deletes the config.
func expectRefValidated(conditions func() []metav1.Condition) {
	Eventually(func() bool {
		cond := meta.FindStatusCondition(conditions(), mcpv1beta1.ConditionOIDCConfigRefValidated)
		return cond != nil && cond.Status == metav1.ConditionTrue
	}, timeout, interval).Should(BeTrue())
}

func createMCPServerReferrer(ns, cfgName string) string {
	const name = "wakeup-server"
	server := &mcpv1beta1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: mcpv1beta1.MCPServerSpec{
			Image: testServerImage,
			OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name:     cfgName,
				Audience: "test-audience",
				Scopes:   []string{"openid"},
			},
		},
	}
	Expect(k8sClient.Create(ctx, server)).To(Succeed())
	expectRefValidated(func() []metav1.Condition {
		got := &mcpv1beta1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return nil
		}
		return got.Status.Conditions
	})
	return name
}

func repointMCPServer(ns, name, cfgName string) {
	Eventually(func() error {
		got := &mcpv1beta1.MCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return err
		}
		got.Spec.OIDCConfigRef.Name = cfgName
		return k8sClient.Update(ctx, got)
	}, timeout, interval).Should(Succeed())
}

func createVMCPReferrer(ns, cfgName string) string {
	const (
		name      = "wakeup-vmcp"
		groupName = "wakeup-vmcp-group"
	)
	group := &mcpv1beta1.MCPGroup{ObjectMeta: metav1.ObjectMeta{Name: groupName, Namespace: ns}}
	Expect(k8sClient.Create(ctx, group)).To(Succeed())

	vmcp := v1beta1test.NewVirtualMCPServer(name, ns,
		v1beta1test.WithVMCPGroupRef(groupName),
		v1beta1test.WithVMCPConfig(vmcpconfig.Config{Group: groupName}),
		v1beta1test.WithVMCPIncomingAuth(&mcpv1beta1.IncomingAuthConfig{
			Type: "oidc",
			OIDCConfigRef: &mcpv1beta1.MCPOIDCConfigReference{
				Name:        cfgName,
				Audience:    "test-vmcp-audience",
				Scopes:      []string{"openid"},
				ResourceURL: "https://mcp-gateway.example.com/mcp",
			},
		}),
	)
	Expect(k8sClient.Create(ctx, vmcp)).To(Succeed())
	expectRefValidated(func() []metav1.Condition {
		got := &mcpv1beta1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return nil
		}
		return got.Status.Conditions
	})
	return name
}

func repointVMCP(ns, name, cfgName string) {
	Eventually(func() error {
		got := &mcpv1beta1.VirtualMCPServer{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return err
		}
		got.Spec.IncomingAuth.OIDCConfigRef.Name = cfgName
		return k8sClient.Update(ctx, got)
	}, timeout, interval).Should(Succeed())
}

func createRemoteProxyReferrer(ns, cfgName string) string {
	const name = "wakeup-proxy"
	proxy := newTestMCPRemoteProxy(name, ns, cfgName)
	Expect(k8sClient.Create(ctx, proxy)).To(Succeed())
	expectRefValidated(func() []metav1.Condition {
		got := &mcpv1beta1.MCPRemoteProxy{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return nil
		}
		return got.Status.Conditions
	})
	return name
}

func repointRemoteProxy(ns, name, cfgName string) {
	Eventually(func() error {
		got := &mcpv1beta1.MCPRemoteProxy{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, got); err != nil {
			return err
		}
		got.Spec.OIDCConfigRef.Name = cfgName
		return k8sClient.Update(ctx, got)
	}, timeout, interval).Should(Succeed())
}
