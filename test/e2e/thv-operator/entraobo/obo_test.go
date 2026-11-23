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

// Shared fixtures, created once in BeforeSuite.
const (
	oidcConfigName    = "entra-oidc"
	proxySecretName   = "proxy-client"
	backendOBOConfig  = "obo-backend"
	vmcpContainerName = "vmcp"
	// proxyRunnerContainer is the proxyrunner container of an MCPServer's
	// proxy Deployment.
	proxyRunnerContainer = "toolhive"
)

// createSharedFixtures creates the MCPOIDCConfig, the middle-tier client
// secret, and the OBO config for the backend scope that every container uses.
func createSharedFixtures() {
	createOIDCConfig(oidcConfigName)
	createClientSecret(proxySecretName, cfg.proxyClientSecret)
	createOBOConfig(backendOBOConfig, proxySecretName, cfg.backendScope)
}

// proxyEndpoint port-forwards to a proxy Service and returns its MCP URL.
func proxyEndpoint(service string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp", portForward(service, mcpPort))
}

// createOBOMCPServer creates a token-recording MCPServer that authenticates
// the incoming Entra user token and exchanges it through oboConfig.
func createOBOMCPServer(name, oboConfig string) {
	server := newRecorderMCPServer(name)
	server.Spec.OIDCConfigRef = oidcRef(oidcConfigName)
	server.Spec.ExternalAuthConfigRef = &mcpv1beta1.ExternalAuthConfigRef{Name: oboConfig}
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

// proxyRunnerLogs returns the log of the MCPServer's running proxyrunner
// container. The proxyrunner logs Entra's OAuth error code and never the
// tokens, but with audit on it logs the user's identity claims, so callers
// match on the log rather than handing it to a Gomega matcher that prints it.
func proxyRunnerLogs(serverName string) (string, error) {
	pods := &corev1.PodList{}
	if err := k8sClient.List(ctx, pods, client.InNamespace(testNamespace), client.MatchingLabels(map[string]string{
		"app.kubernetes.io/name":     "mcpserver",
		"app.kubernetes.io/instance": serverName,
	})); err != nil {
		return "", fmt.Errorf("listing pods of MCPServer %s: %w", serverName, err)
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.Name == proxyRunnerContainer {
				return testutil.GetPodLogs(ctx, testNamespace, pod.Name, proxyRunnerContainer, false)
			}
		}
	}
	return "", fmt.Errorf("no running pod of MCPServer %s has a %q container", serverName, proxyRunnerContainer)
}

// expectProxyRunnerLogContains fails g unless the MCPServer's proxyrunner log
// contains want. The log itself stays out of the failure message.
func expectProxyRunnerLogContains(g gomega.Gomega, serverName, want string) {
	logs, err := proxyRunnerLogs(serverName)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	g.Expect(strings.Contains(logs, want)).To(gomega.BeTrue(),
		"the proxyrunner log of %s has no %q line", serverName, want)
}

// createGroup creates an MCPGroup and waits for it.
func createGroup(name string) {
	virtualmcp.CreateMCPGroupAndWait(ctx, k8sClient, name, testNamespace,
		"Entra OBO live-tenant e2e", timeout, pollInterval)
}

var _ = ginkgo.Describe("Entra OBO: MCPServer", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const serverName = "obo-server"
	var (
		endpoint string
		// seen is what the backend recorded after one call, captured here so
		// each spec also runs on its own (e.g. with --focus).
		seen []string
	)

	ginkgo.BeforeAll(func() {
		createOBOMCPServer(serverName, backendOBOConfig)
		endpoint = proxyEndpoint(controllerutil.CreateProxyServiceName(serverName))
		callPing(endpoint, user.token, pingTool)
		seen = recordedTokens("mcp-" + serverName)
	})

	ginkgo.It("exchanges the user token and the backend receives an Entra token for its audience", func() {
		expectExchangedToken(seen)
	})

	ginkgo.It("rejects a token for another audience with 401 at the incoming OIDC layer", func() {
		// The exchanged token is a valid Entra token for the backend, not for
		// the middle-tier app, so the proxy's OIDC validation must refuse it.
		gomega.Expect(fingerprints(seen)).NotTo(gomega.BeEmpty())
		gomega.Expect(initializeStatus(endpoint, seen[0])).To(gomega.Equal(http.StatusUnauthorized))
	})
})

var _ = ginkgo.Describe("Entra OBO: audience instead of scopes", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const serverName = "obo-audience"
	var endpoint string

	ginkgo.BeforeAll(func() {
		// The backend's Application ID URI, from the .default scope. ToolHive
		// must turn a bare audience into <audience>/.default for Entra.
		audience, ok := strings.CutSuffix(cfg.backendScope, "/.default")
		if !ok {
			ginkgo.Skip(envBackendScope + " is not a .default scope, so the backend audience cannot be derived")
		}
		createOBOAudienceConfig(serverName, proxySecretName, audience)
		createOBOMCPServer(serverName, serverName)
		endpoint = proxyEndpoint(controllerutil.CreateProxyServiceName(serverName))
	})

	ginkgo.It("exchanges the user token for the backend named by audience alone", func() {
		callPing(endpoint, user.token, pingTool)
		expectExchangedToken(recordedTokens("mcp-" + serverName))
	})
})

var _ = ginkgo.Describe("Entra OBO: MCPRemoteProxy", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	const (
		proxyName    = "obo-remote"
		recorderName = "remote-recorder"
	)
	var endpoint string

	ginkgo.BeforeAll(func() {
		remoteURL := deployRecorder(recorderName)

		gomega.Expect(k8sClient.Create(ctx, &mcpv1beta1.MCPRemoteProxy{
			ObjectMeta: metav1.ObjectMeta{Name: proxyName, Namespace: testNamespace},
			Spec: mcpv1beta1.MCPRemoteProxySpec{
				RemoteURL:             remoteURL,
				AllowPrivateEndpoint:  true,
				Transport:             "streamable-http",
				ProxyPort:             mcpPort,
				OIDCConfigRef:         oidcRef(oidcConfigName),
				ExternalAuthConfigRef: &mcpv1beta1.ExternalAuthConfigRef{Name: backendOBOConfig},
			},
		})).To(gomega.Succeed())
		// Failed is not terminal while a referenced config is still being
		// validated, so keep polling, as waitMCPServerReady does.
		gomega.Eventually(func() error {
			p := &mcpv1beta1.MCPRemoteProxy{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: proxyName, Namespace: testNamespace}, p); err != nil {
				return err
			}
			if p.Status.Phase != mcpv1beta1.MCPRemoteProxyPhaseReady {
				return fmt.Errorf("MCPRemoteProxy phase %q: %s", p.Status.Phase, p.Status.Message)
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

	ginkgo.It("mounts a distinct client-secret env var per OBO config, each from its own Secret", func() {
		deployment := &appsv1.Deployment{}
		gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: vmcpName, Namespace: testNamespace},
			deployment)).To(gomega.Succeed())
		// Env var name -> the Secret it reads. Both Secrets hold the same
		// value, so the exchange alone cannot tell a miswired pair apart.
		secretByEnv := map[string]string{}
		for _, c := range deployment.Spec.Template.Spec.Containers {
			if c.Name != vmcpContainerName {
				continue
			}
			for _, e := range c.Env {
				if !strings.HasPrefix(e.Name, "TOOLHIVE_OBO_CLIENT_SECRET") {
					continue
				}
				gomega.Expect(e.ValueFrom).NotTo(gomega.BeNil(), "%s must come from a Secret", e.Name)
				gomega.Expect(e.ValueFrom.SecretKeyRef).NotTo(gomega.BeNil(), "%s must come from a Secret", e.Name)
				secretByEnv[e.Name] = e.ValueFrom.SecretKeyRef.Name
			}
		}
		want := map[string]string{}
		for _, b := range backends {
			want[controllerutil.GenerateUniqueOBOSecretEnvVarName(b.oboConfig)] = b.secret
		}
		gomega.Expect(secretByEnv).To(gomega.Equal(want))
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

	// Pending: discovered mode cannot reach an OBO MCPServer today. vMCP
	// applies the backend's OBO config and sends the exchanged token (aud =
	// the backend) to the MCPServer's proxy. The operator requires OIDC on
	// every OBO MCPServer, and that OIDC layer expects the middle-tier app as
	// the audience, so the proxy answers 401 before any second exchange.
	// Enable once discovered mode stops applying the backend's OBO config
	// twice.
	ginkgo.PIt("exchanges the user token for an MCPServer backend")
})

var _ = ginkgo.Describe("Entra OBO: exchange failures", ginkgo.Label(entraLiveLabel), ginkgo.Ordered, func() {
	ginkgo.It("answers 502 when the client secret is wrong, and the server still starts", func() {
		const name = "obo-wrong-secret"
		createClientSecret(name, "not-the-client-secret")
		createOBOConfig(name, name, cfg.backendScope)
		createOBOMCPServer(name, name) // waits for Ready
		endpoint := proxyEndpoint(controllerutil.CreateProxyServiceName(name))
		// A 502 alone could also come from the proxy reaching a backend that
		// is still starting, or from Entra being unreachable. Require the
		// proxyrunner to have logged Entra's invalid_client rejection, and
		// retry until it has, since Ready does not mean the backend serves.
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(initializeStatus(endpoint, user.token)).To(gomega.Equal(http.StatusBadGateway))
			expectProxyRunnerLogContains(g, name, "invalid_client")
		}, 2*time.Minute, pollInterval).Should(gomega.Succeed())
	})

	ginkgo.It("answers 401 when the scope is not consented for the middle-tier app", func() {
		if cfg.unconsentedScope == "" {
			ginkgo.Skip(envUnconsentedScope + " not set")
		}
		const name = "obo-unconsented"
		createOBOConfig(name, proxySecretName, cfg.unconsentedScope)
		createOBOMCPServer(name, name)
		endpoint := proxyEndpoint(controllerutil.CreateProxyServiceName(name))
		gomega.Expect(initializeStatus(endpoint, user.token)).To(gomega.Equal(http.StatusUnauthorized))
	})

	ginkgo.It("answers 403 and emits an audit event when conditional access requires interaction", func() {
		if cfg.conditionalAccessScope == "" {
			ginkgo.Skip(envConditionalAccessScope + " not set")
		}
		const name = "obo-conditional-access"
		createOBOConfig(name, proxySecretName, cfg.conditionalAccessScope)
		// The OBO middleware emits its audit event whether or not the
		// MCPServer enables audit, so the server needs no audit config.
		createOBOMCPServer(name, name)
		endpoint := proxyEndpoint(controllerutil.CreateProxyServiceName(name))
		gomega.Expect(initializeStatus(endpoint, user.token)).To(gomega.Equal(http.StatusForbidden))

		gomega.Eventually(func(g gomega.Gomega) {
			expectProxyRunnerLogContains(g, name, "obo_token_exchange")
		}, time.Minute, pollInterval).Should(gomega.Succeed())
	})
})
