// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1/v1beta1test"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

func TestMCPServerAuthServerTLSListenerPorts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled"},
		{name: "enabled", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			config := externalAuthConfigWithTLSListener(test.enabled)
			server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithExternalAuthConfigRef(config.Name))
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
			reconciler := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)

			deployment, err := reconciler.deploymentForMCPServer(t.Context(), server, "checksum")
			require.NoError(t, err)
			assert.Equal(t, expectedContainerPorts(server.GetProxyPort(), test.enabled), deployment.Spec.Template.Spec.Containers[0].Ports)

			service := reconciler.serviceForMCPServer(t.Context(), server, test.enabled)
			require.NotNil(t, service)
			assert.Equal(t, expectedServicePorts(server.GetProxyPort(), test.enabled), service.Spec.Ports)
			assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, server, "checksum"))
			assert.False(t, serviceNeedsUpdate(service, server, test.enabled))
		})
	}
}

func TestMCPServerAuthServerTLSListenerPortDrift(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		current bool
		desired bool
	}{
		{name: "enable listener", desired: true},
		{name: "disable listener", current: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			config := externalAuthConfigWithTLSListener(test.current)
			server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithExternalAuthConfigRef(config.Name))
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
			reconciler := newTestMCPServerReconciler(client, scheme, kubernetes.PlatformKubernetes)
			deployment, err := reconciler.deploymentForMCPServer(t.Context(), server, "checksum")
			require.NoError(t, err)
			service := reconciler.serviceForMCPServer(t.Context(), server, test.current)

			config.Spec.EmbeddedAuthServer.TLSListener = tlsListenerConfig(test.desired)
			require.NoError(t, client.Update(t.Context(), config))

			assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, server, "checksum"))
			assert.True(t, serviceNeedsUpdate(service, server, test.desired))

			desiredDeployment, err := reconciler.deploymentForMCPServer(t.Context(), server, "checksum")
			require.NoError(t, err)
			assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), desiredDeployment, server, "checksum"))
			assert.False(t, serviceNeedsUpdate(reconciler.serviceForMCPServer(t.Context(), server, test.desired), server, test.desired))
		})
	}
}

func TestMCPRemoteProxyAuthServerTLSListenerPorts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled"},
		{name: "enabled", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			config := externalAuthConfigWithTLSListener(test.enabled)
			proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef(config.Name))
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
			reconciler := &MCPRemoteProxyReconciler{Client: client, Scheme: scheme, PlatformDetector: ctrlutil.NewSharedPlatformDetector()}

			deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "checksum")
			require.NotNil(t, deployment)
			assert.Equal(t, expectedContainerPorts(int32(proxy.GetProxyPort()), test.enabled), deployment.Spec.Template.Spec.Containers[0].Ports)

			service := reconciler.serviceForMCPRemoteProxy(t.Context(), proxy, test.enabled)
			require.NotNil(t, service)
			assert.Equal(t, expectedServicePorts(int32(proxy.GetProxyPort()), test.enabled), service.Spec.Ports)
			assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "checksum"))
			assert.False(t, reconciler.serviceNeedsUpdate(service, proxy, test.enabled))
		})
	}
}

func TestMCPRemoteProxyAuthServerTLSListenerPortDrift(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		current bool
		desired bool
	}{
		{name: "enable listener", desired: true},
		{name: "disable listener", current: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheme := testutil.NewScheme(t)
			config := externalAuthConfigWithTLSListener(test.current)
			proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef(config.Name))
			client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(config).Build()
			reconciler := &MCPRemoteProxyReconciler{Client: client, Scheme: scheme, PlatformDetector: ctrlutil.NewSharedPlatformDetector()}
			deployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "checksum")
			require.NotNil(t, deployment)
			service := reconciler.serviceForMCPRemoteProxy(t.Context(), proxy, test.current)

			config.Spec.EmbeddedAuthServer.TLSListener = tlsListenerConfig(test.desired)
			require.NoError(t, client.Update(t.Context(), config))

			assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, proxy, "checksum"))
			assert.True(t, reconciler.serviceNeedsUpdate(service, proxy, test.desired))

			desiredDeployment := reconciler.deploymentForMCPRemoteProxy(t.Context(), proxy, "checksum")
			require.NotNil(t, desiredDeployment)
			assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), desiredDeployment, proxy, "checksum"))
			assert.False(t, reconciler.serviceNeedsUpdate(reconciler.serviceForMCPRemoteProxy(t.Context(), proxy, test.desired), proxy, test.desired))
		})
	}
}

func TestVirtualMCPServerAuthServerTLSListenerPorts(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled"},
		{name: "enabled", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			vmcp := v1beta1test.NewVirtualMCPServer("vmcp", "default",
				v1beta1test.WithVMCPGroupRef("group"),
				v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: tlsListenerConfig(test.enabled)}),
			)
			reconciler := &VirtualMCPServerReconciler{Scheme: testutil.NewScheme(t), PlatformDetector: ctrlutil.NewSharedPlatformDetector()}
			deployment := reconciler.deploymentForVirtualMCPServer(t.Context(), vmcp, "checksum", "", nil, []workloads.TypedWorkload{})
			require.NotNil(t, deployment)
			assert.Equal(t, expectedContainerPorts(vmcpDefaultPort, test.enabled), deployment.Spec.Template.Spec.Containers[0].Ports)

			service := reconciler.serviceForVirtualMCPServer(t.Context(), vmcp)
			require.NotNil(t, service)
			assert.Equal(t, expectedServicePorts(vmcpDefaultPort, test.enabled), service.Spec.Ports)
			assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, vmcp, "checksum", "", nil, []workloads.TypedWorkload{}))
			assert.False(t, reconciler.serviceNeedsUpdate(service, vmcp))
		})
	}
}

func TestVirtualMCPServerAuthServerTLSListenerPortDrift(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		current bool
		desired bool
	}{
		{name: "enable listener", desired: true},
		{name: "disable listener", current: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			vmcp := v1beta1test.NewVirtualMCPServer("vmcp", "default",
				v1beta1test.WithVMCPGroupRef("group"),
				v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: tlsListenerConfig(test.current)}),
			)
			reconciler := &VirtualMCPServerReconciler{Scheme: testutil.NewScheme(t), PlatformDetector: ctrlutil.NewSharedPlatformDetector()}
			deployment := reconciler.deploymentForVirtualMCPServer(t.Context(), vmcp, "checksum", "", nil, []workloads.TypedWorkload{})
			require.NotNil(t, deployment)
			service := reconciler.serviceForVirtualMCPServer(t.Context(), vmcp)

			vmcp.Spec.AuthServerConfig.TLSListener = tlsListenerConfig(test.desired)
			assert.True(t, reconciler.deploymentNeedsUpdate(t.Context(), deployment, vmcp, "checksum", "", nil, []workloads.TypedWorkload{}))
			assert.True(t, reconciler.serviceNeedsUpdate(service, vmcp))

			desiredDeployment := reconciler.deploymentForVirtualMCPServer(t.Context(), vmcp, "checksum", "", nil, []workloads.TypedWorkload{})
			require.NotNil(t, desiredDeployment)
			assert.False(t, reconciler.deploymentNeedsUpdate(t.Context(), desiredDeployment, vmcp, "checksum", "", nil, []workloads.TypedWorkload{}))
			assert.False(t, reconciler.serviceNeedsUpdate(reconciler.serviceForVirtualMCPServer(t.Context(), vmcp), vmcp))
		})
	}
}

func TestVirtualMCPServerLoadBalancerServiceNodePortsDoNotDrift(t *testing.T) {
	t.Parallel()

	vmcp := v1beta1test.NewVirtualMCPServer("vmcp", "default",
		v1beta1test.WithVMCPGroupRef("group"),
		v1beta1test.MutateVMCP(func(vmcp *mcpv1beta1.VirtualMCPServer) {
			vmcp.Spec.ServiceType = string(corev1.ServiceTypeLoadBalancer)
			vmcp.Spec.AuthServerConfig = &mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: tlsListenerConfig(true)}
		}),
	)
	reconciler := &VirtualMCPServerReconciler{Scheme: testutil.NewScheme(t)}
	service := reconciler.serviceForVirtualMCPServer(t.Context(), vmcp)
	require.Len(t, service.Spec.Ports, 2)
	service.Spec.Ports[0].NodePort = 30080
	service.Spec.Ports[1].NodePort = 30443

	assert.False(t, reconciler.serviceNeedsUpdate(service, vmcp))
}

func externalAuthConfigWithTLSListener(enabled bool) *mcpv1beta1.MCPExternalAuthConfig {
	return &mcpv1beta1.MCPExternalAuthConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "auth", Namespace: "default"},
		Spec: mcpv1beta1.MCPExternalAuthConfigSpec{
			Type: mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
			EmbeddedAuthServer: &mcpv1beta1.EmbeddedAuthServerConfig{
				TLSListener: tlsListenerConfig(enabled),
			},
		},
	}
}

func tlsListenerConfig(enabled bool) *mcpv1beta1.TLSListenerConfig {
	if !enabled {
		return nil
	}
	return &mcpv1beta1.TLSListenerConfig{
		CertificateSecretRef: &mcpv1beta1.SecretKeyRef{Name: "listener", Key: "tls.crt"},
		PrivateKeySecretRef:  &mcpv1beta1.SecretKeyRef{Name: "listener", Key: "tls.key"},
	}
}

func expectedContainerPorts(httpPort int32, tlsListener bool) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{{Name: "http", ContainerPort: httpPort, Protocol: corev1.ProtocolTCP}}
	if tlsListener {
		ports = append(ports, ctrlutil.TLSListenerContainerPort())
	}
	return ports
}

func expectedServicePorts(httpPort int32, tlsListener bool) []corev1.ServicePort {
	ports := []corev1.ServicePort{{Name: "http", Port: httpPort, TargetPort: intstrFromInt32(httpPort), Protocol: corev1.ProtocolTCP}}
	if tlsListener {
		ports = append(ports, ctrlutil.TLSListenerServicePort())
	}
	return ports
}

func intstrFromInt32(port int32) intstr.IntOrString {
	return intstr.FromInt32(port)
}
