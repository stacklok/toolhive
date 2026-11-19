// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package entraobo

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/testutil"
	"github.com/stacklok/toolhive/test/e2e/thv-operator/virtualmcp"
)

// Shared fixtures, created once by the first container that needs them.
const (
	oidcConfigName    = "entra-oidc"
	proxySecretName   = "proxy-client"
	backendOBOConfig  = "obo-backend"
	vmcpContainerName = "vmcp"
)

// ensureSharedFixtures creates the MCPOIDCConfig, the middle-tier client
// secret, and the OBO config for the backend scope. Containers run in order on
// one process (`--procs=1`), so the first caller creates them.
var sharedFixturesCreated bool

func ensureSharedFixtures() {
	if sharedFixturesCreated {
		return
	}
	createOIDCConfig(oidcConfigName)
	createClientSecret(proxySecretName, cfg.proxyClientSecret)
	createOBOConfig(backendOBOConfig, proxySecretName, cfg.backendScope)
	sharedFixturesCreated = true
}

// proxyEndpoint port-forwards to a proxy Service and returns its MCP URL.
func proxyEndpoint(service string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", portForward(service, 8080))
}

// createOBOMCPServer creates a token-recording MCPServer that authenticates
// the incoming Entra user token and exchanges it through oboConfig.
func createOBOMCPServer(name, oboConfig string, audit bool) {
	server := newRecorderMCPServer(name)
	server.Spec.OIDCConfigRef = oidcRef(oidcConfigName)
	server.Spec.ExternalAuthConfigRef = &mcpv1beta1.ExternalAuthConfigRef{Name: oboConfig}
	if audit {
		server.Spec.Audit = &mcpv1beta1.AuditConfig{Enabled: true}
	}
	gomega.Expect(k8sClient.Create(ctx, server)).To(gomega.Succeed())
	waitMCPServerReady(name)
}

// createVMCP creates a VirtualMCPServer over group that authenticates the
// Entra user token, waits for it, and returns its port-forwarded MCP URL.
func createVMCP(name, group string, outgoing *mcpv1beta1.OutgoingAuthConfig) string {
	gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.VirtualMCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: mcpv1beta1.VirtualMCPServerSpec{
			GroupRef:     &mcpv1beta1.MCPGroupRef{Name: group},
			Config:       vmcpconfig.Config{Group: group},
			IncomingAuth: &mcpv1beta1.IncomingAuthConfig{Type: "oidc", OIDCConfigRef: oidcRef(oidcConfigName)},
			OutgoingAuth: outgoing,
		},
	})).To(gomega.Succeed())
	virtualmcp.WaitForVirtualMCPServerReady(ctx, k8sClient, name, testNamespace, timeout, pollInterval)

	service := &corev1.Service{}
	gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
		Name: virtualmcp.VMCPServiceName(name), Namespace: testNamespace,
	}, service)).To(gomega.Succeed())
	gomega.Expect(service.Spec.Ports).NotTo(gomega.BeEmpty())
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", portForward(service.Name, service.Spec.Ports[0].Port))
}

// createGroup creates an MCPGroup and waits for it.
func createGroup(name string) {
	virtualmcp.CreateMCPGroupAndWait(ctx, k8sClient, name, testNamespace,
		"Entra OBO live-tenant e2e", timeout, pollInterval)
}

var _ = ginkgo.Describe("Entra OBO: MCPServer", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const serverName = "obo-server"
	var endpoint string

	ginkgo.BeforeAll(func() {
		ensureSharedFixtures()
		createOBOMCPServer(serverName, backendOBOConfig, false)
		endpoint = proxyEndpoint(controllerutil.CreateProxyServiceName(serverName))
	})

	ginkgo.It("exchanges the user token and the backend receives an Entra token for its audience", func() {
		callPing(endpoint, user.token, pingTool)
		expectExchangedToken(recordedTokens("mcp-" + serverName))
	})

	ginkgo.It("rejects a token for another audience with 401 at the incoming OIDC layer", func() {
		// The exchanged token is a valid Entra token for the backend, not for
		// the middle-tier app, so the proxy's OIDC validation must refuse it.
		seen := recordedTokens("mcp-" + serverName)
		gomega.Expect(fingerprints(seen)).NotTo(gomega.BeEmpty())
		gomega.Expect(initializeStatus(endpoint, seen[0])).To(gomega.Equal(http.StatusUnauthorized))
	})
})

var _ = ginkgo.Describe("Entra OBO: MCPRemoteProxy", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const (
		proxyName    = "obo-remote"
		recorderName = "remote-recorder"
	)
	var endpoint string

	ginkgo.BeforeAll(func() {
		ensureSharedFixtures()
		remoteURL := deployRecorder(recorderName)

		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			Spec: mcpv1beta1.MCPRemoteProxySpec{
				RemoteURL:             remoteURL,
				AllowPrivateEndpoint:  true,
				Transport:             "streamable-http",
				ProxyPort:             8080,
				OIDCConfigRef:         oidcRef(oidcConfigName),
				ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: backendOBOConfig},
			},
		})).To(gomega.Succeed())
		gomega.Eventually(func() error {
			p := &mcpv1beta1.MCPRemoteProxy{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: proxyName, Namespace: testNamespace}, p); err != nil {
				return err
			}
			if p.Status.Phase == mcpv1beta1.MCPRemoteProxyPhaseFailed {
				return gomega.StopTrying("MCPRemoteProxy failed: " + p.Status.Message)
			}
			if p.Status.Phase != mcpv1beta1.MCPRemoteProxyPhaseReady {
				return fmt.Errorf("MCPRemoteProxy phase %q", p.Status.Phase)
			}
			return nil
		}, timeout, pollInterval).Should(gomega.Succeed())

		endpoint = proxyEndpoint("mcp-" + proxyName + "-remote-proxy")
	})

	ginkgo.It("exchanges the user token and the remote backend receives an Entra token for its audience", func() {
		callPing(endpoint, user.token, pingTool)
		expectExchangedToken(recordedTokens(recorderName))
	})
})

var _ = ginkgo.Describe("Entra OBO: VirtualMCPServer inline outgoingAuth", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const (
		groupName = "obo-inline-group"
		vmcpName  = "obo-inline-vmcp"
	)
	// Two backends, each with its own OBO config and its own Secret, so the
	// vMCP pod must carry two distinct client-secret env vars.
	backends := []struct{ server, oboConfig, secret string }{
		{"inline-a", "obo-inline-a", "obo-inline-a-client"},
		{"inline-b", "obo-inline-b", "obo-inline-b-client"},
	}
	var endpoint string

	ginkgo.BeforeAll(func() {
		ensureSharedFixtures()
		createGroup(groupName)

		outgoing := &mcpv1beta1.OutgoingAuthConfig{
			Source:   "inline",
			Backends: map[string]mcpv1beta1.BackendAuthConfig{},
		}
		for _, b := range backends {
			createClientSecret(b.secret, cfg.proxyClientSecret)
			createOBOConfig(b.oboConfig, b.secret, cfg.backendScope)

			server := newRecorderMCPServer(b.server)
			server.Spec.GroupRef = &mcpv1beta1.MCPGroupRef{Name: groupName}
			gomega.Expect(k8sClient.Create(ctx, server)).To(gomega.Succeed())
			waitMCPServerReady(b.server)

			outgoing.Backends[b.server] = mcpv1beta1.BackendAuthConfig{
				Type:                  "externalAuthConfigRef",
				ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: b.oboConfig},
			}
		}
		endpoint = createVMCP(vmcpName, groupName, outgoing)
	})

	ginkgo.It("mounts a distinct client-secret env var per OBO config", func() {
		deployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: testNamespace},
			deployment)).To(gomega.Succeed())
		var envNames []string
		for _, c := range deployment.Spec.Template.Spec.Containers {
			if c.Name != vmcpContainerName {
				continue
			}
			for _, e := range c.Env {
				if strings.HasPrefix(e.Name, "TOOLHIVE_OBO_CLIENT_SECRET") {
					gomega.Expect(e.ValueFrom).NotTo(gomega.BeNil(), "%s must come from a Secret", e.Name)
					envNames = append(envNames, e.Name)
				}
			}
		}
		gomega.Expect(envNames).To(gomega.ConsistOf(
			controllerutil.GenerateUniqueOBOSecretEnvVarName(backends[0].oboConfig),
			controllerutil.GenerateUniqueOBOSecretEnvVarName(backends[1].oboConfig),
		))
	})

	ginkgo.It("exchanges the user token for each backend", func() {
		for _, b := range backends {
			callPing(endpoint, user.token, b.server+"_"+pingTool)
			expectExchangedToken(recordedTokens("mcp-" + b.server))
		}
	})
})

var _ = ginkgo.Describe("Entra OBO: VirtualMCPServer discovered outgoingAuth", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const (
		groupName    = "obo-discovered-group"
		vmcpName     = "obo-discovered-vmcp"
		entryName    = "obo-entry"
		recorderName = "entry-recorder"
	)
	var endpoint string

	ginkgo.BeforeAll(func() {
		ensureSharedFixtures()
		createGroup(groupName)
		remoteURL := deployRecorder(recorderName)

		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPServerEntry{
			ObjectMeta: metav1.ObjectMeta{Name: entryName, Namespace: testNamespace},
			Spec: mcpv1beta1.MCPServerEntrySpec{
				RemoteURL:             remoteURL,
				AllowPrivateEndpoint:  true,
				Transport:             "streamable-http",
				GroupRef:              &mcpv1beta1.MCPGroupRef{Name: groupName},
				ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: backendOBOConfig},
			},
		})).To(gomega.Succeed())
		gomega.Eventually(func() error {
			e := &mcpv1beta1.MCPServerEntry{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: entryName, Namespace: testNamespace}, e); err != nil {
				return err
			}
			if e.Status.Phase != mcpv1beta1.MCPServerEntryPhaseValid {
				return fmt.Errorf("MCPServerEntry phase %q", e.Status.Phase)
			}
			return nil
		}, timeout, pollInterval).Should(gomega.Succeed())

		endpoint = createVMCP(vmcpName, groupName, &mcpv1beta1.OutgoingAuthConfig{Source: "discovered"})
	})

	ginkgo.It("exchanges the user token for an MCPServerEntry backend", func() {
		callPing(endpoint, user.token, entryName+"_"+pingTool)
		expectExchangedToken(recordedTokens(recorderName))
	})

	// Pending: vMCP reaches a discovered MCPServer through its proxy. An OBO
	// MCPServer must also authenticate incoming requests, so its proxy
	// re-validates the token vMCP already exchanged and exchanges it again,
	// which Entra refuses. Enable once discovered mode stops double-applying
	// the backend's OBO config.
	ginkgo.PIt("exchanges the user token for an MCPServer backend")
})

var _ = ginkgo.Describe("Entra OBO: exchange failures", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	ginkgo.BeforeAll(ensureSharedFixtures)

	ginkgo.It("answers 502 when the client secret is wrong, and the server still starts", func() {
		const name = "obo-wrong-secret"
		createClientSecret(name, "not-the-client-secret")
		createOBOConfig(name, name, cfg.backendScope)
		createOBOMCPServer(name, name, false) // waits for Ready
		endpoint := proxyEndpoint(controllerutil.CreateProxyServiceName(name))
		gomega.Expect(initializeStatus(endpoint, user.token)).To(gomega.Equal(http.StatusBadGateway))
	})

	ginkgo.It("answers 401 when the scope is not consented for the middle-tier app", func() {
		if cfg.unconsentedScope == "" {
			ginkgo.Skip(envUnconsentedScope + " not set")
		}
		const name = "obo-unconsented"
		createOBOConfig(name, proxySecretName, cfg.unconsentedScope)
		createOBOMCPServer(name, name, false)
		endpoint := proxyEndpoint(controllerutil.CreateProxyServiceName(name))
		gomega.Expect(initializeStatus(endpoint, user.token)).To(gomega.Equal(http.StatusUnauthorized))
	})

	ginkgo.It("answers 403 and emits an audit event when conditional access requires interaction", func() {
		if cfg.conditionalAccessScope == "" {
			ginkgo.Skip(envConditionalAccessScope + " not set")
		}
		const name = "obo-conditional-access"
		createOBOConfig(name, proxySecretName, cfg.conditionalAccessScope)
		createOBOMCPServer(name, name, true)
		endpoint := proxyEndpoint(controllerutil.CreateProxyServiceName(name))
		gomega.Expect(initializeStatus(endpoint, user.token)).To(gomega.Equal(http.StatusForbidden))

		pods := &corev1.PodList{}
		gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(testNamespace), client.MatchingLabels(map[string]string{
			"app.kubernetes.io/name":     "mcpserver",
			"app.kubernetes.io/instance": name,
		}))).To(gomega.Succeed())
		gomega.Expect(pods.Items).NotTo(gomega.BeEmpty())
		gomega.Eventually(func() (string, error) {
			return testutil.GetPodLogs(ctx, testNamespace, pods.Items[0].Name, "toolhive", false)
		}, time.Minute, pollInterval).Should(gomega.ContainSubstring("obo_token_exchange"))
	})
})
