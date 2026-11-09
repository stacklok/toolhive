// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

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

func TestVirtualMCPServerPodTemplateSpecPortDrift(t *testing.T) {
	t.Parallel()

	both := []bool{false, true}
	for _, test := range []struct {
		name         string
		podTemplate  string
		tlsListeners []bool
	}{
		{
			name:         "extra podTemplateSpec port is not drift",
			podTemplate:  `{"spec":{"containers":[{"name":"vmcp","ports":[{"name":"metrics","containerPort":9090}]}]}}`,
			tlsListeners: both,
		},
		// The podTemplateSpec merge key is containerPort, so this patch renames
		// the operator's http port. Ports are matched by number and protocol, so a
		// rename is tolerated rather than reported as drift on every reconcile.
		{
			name:         "renamed http port is not drift",
			podTemplate:  `{"spec":{"containers":[{"name":"vmcp","ports":[{"containerPort":4483,"name":"x"}]}]}}`,
			tlsListeners: both,
		},
		{
			name:         "https-auth renamed to https with the listener on is not drift",
			podTemplate:  `{"spec":{"containers":[{"name":"vmcp","ports":[{"containerPort":8443,"name":"https"}]}]}}`,
			tlsListeners: []bool{true},
		},
		{
			name:         "https-auth on another number with the listener off is not drift",
			podTemplate:  `{"spec":{"containers":[{"name":"vmcp","ports":[{"containerPort":9443,"name":"https-auth"}]}]}}`,
			tlsListeners: []bool{false},
		},
		{
			name:         "extra port on 8443 with the listener off is not drift",
			podTemplate:  `{"spec":{"containers":[{"name":"vmcp","ports":[{"containerPort":8443,"name":"https"}]}]}}`,
			tlsListeners: []bool{false},
		},
	} {
		for _, tlsListener := range test.tlsListeners {
			t.Run(fmt.Sprintf("%s tlsListener=%t", test.name, tlsListener), func(t *testing.T) {
				t.Parallel()

				vmcp := v1beta1test.NewVirtualMCPServer("vmcp", "default",
					v1beta1test.WithVMCPGroupRef("group"),
					v1beta1test.WithVMCPAuthServerConfig(&mcpv1beta1.EmbeddedAuthServerConfig{TLSListener: tlsListenerConfig(tlsListener)}),
					v1beta1test.WithVMCPPodTemplateSpec(&runtime.RawExtension{Raw: []byte(test.podTemplate)}),
				)
				reconciler := &VirtualMCPServerReconciler{Scheme: testutil.NewScheme(t), PlatformDetector: ctrlutil.NewSharedPlatformDetector()}

				deployment := reconciler.deploymentForVirtualMCPServer(t.Context(), vmcp, "checksum", "", nil, []workloads.TypedWorkload{})
				require.NotNil(t, deployment)
				assert.False(t, reconciler.containerNeedsUpdate(t.Context(), deployment, vmcp, nil, []workloads.TypedWorkload{}))
			})
		}
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
		ports = append(ports, corev1.ContainerPort{Name: "https-auth", ContainerPort: 8443, Protocol: corev1.ProtocolTCP})
	}
	return ports
}

func expectedServicePorts(httpPort int32, tlsListener bool) []corev1.ServicePort {
	ports := []corev1.ServicePort{{Name: "http", Port: httpPort, TargetPort: intstr.FromInt32(httpPort), Protocol: corev1.ProtocolTCP}}
	if tlsListener {
		appProtocol := "https"
		ports = append(ports, corev1.ServicePort{
			Name:        "https-auth",
			Port:        8443,
			TargetPort:  intstr.FromInt32(8443),
			Protocol:    corev1.ProtocolTCP,
			AppProtocol: &appProtocol,
		})
	}
	return ports
}

// newClientFailingAuthConfigGet returns a fake client that fails only the Get
// of an MCPExternalAuthConfig, with an error that is not NotFound.
func newClientFailingAuthConfigGet(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(testutil.NewScheme(t)).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*mcpv1beta1.MCPExternalAuthConfig); ok {
					return errors.New("boom")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
}

// newClientFailingFirstAuthConfigGet returns a fake client holding objs whose
// first Get of an MCPExternalAuthConfig fails with an error that is not
// NotFound. Later Gets succeed, so a caller that ignores the first failure
// sees a valid auth config instead of failing on its own.
func newClientFailingFirstAuthConfigGet(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	var failed atomic.Bool
	return fake.NewClientBuilder().WithScheme(testutil.NewScheme(t)).WithObjects(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*mcpv1beta1.MCPExternalAuthConfig); ok && failed.CompareAndSwap(false, true) {
					return errors.New("boom")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
}

func TestMCPServerAuthServerTLSListenerLookupFailure(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)
	server := v1beta1test.NewMCPServer("server", "default", v1beta1test.WithExternalAuthConfigRef("auth"))
	working := newTestMCPServerReconciler(fake.NewClientBuilder().WithScheme(scheme).WithObjects(externalAuthConfigWithTLSListener(false)).Build(), scheme, kubernetes.PlatformKubernetes)
	failing := newTestMCPServerReconciler(newClientFailingAuthConfigGet(t), scheme, kubernetes.PlatformKubernetes)
	failingFirst := newTestMCPServerReconciler(
		newClientFailingFirstAuthConfigGet(t, externalAuthConfigWithTLSListener(false)), scheme, kubernetes.PlatformKubernetes)

	existing, err := working.deploymentForMCPServer(t.Context(), server, "checksum")
	require.NoError(t, err)
	require.False(t, working.deploymentNeedsUpdate(t.Context(), existing, server, "checksum"))

	deployment, err := failing.deploymentForMCPServer(t.Context(), server, "checksum")
	require.ErrorContains(t, err, "failed to resolve auth server TLS listener")
	assert.Nil(t, deployment)
	assert.True(t, failingFirst.deploymentNeedsUpdate(t.Context(), existing, server, "checksum"))
}

func TestMCPRemoteProxyAuthServerTLSListenerLookupFailure(t *testing.T) {
	t.Parallel()

	scheme := testutil.NewScheme(t)
	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef("auth"))
	working := &MCPRemoteProxyReconciler{
		Client:           fake.NewClientBuilder().WithScheme(scheme).WithObjects(externalAuthConfigWithTLSListener(false)).Build(),
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}
	failing := &MCPRemoteProxyReconciler{
		Client:           newClientFailingAuthConfigGet(t, proxy),
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}
	failingFirst := &MCPRemoteProxyReconciler{
		Client:           newClientFailingFirstAuthConfigGet(t, externalAuthConfigWithTLSListener(false)),
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}

	existing := working.deploymentForMCPRemoteProxy(t.Context(), proxy, "checksum")
	require.NotNil(t, existing)
	require.False(t, working.generatedContainerNeedsUpdate(t.Context(), existing, proxy))

	freshFailingFirst := &MCPRemoteProxyReconciler{
		Client:           newClientFailingFirstAuthConfigGet(t, externalAuthConfigWithTLSListener(false)),
		Scheme:           scheme,
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}
	assert.Nil(t, freshFailingFirst.deploymentForMCPRemoteProxy(t.Context(), proxy, "checksum"))
	assert.True(t, failingFirst.generatedContainerNeedsUpdate(t.Context(), existing, proxy))

	_, err := failing.ensureService(t.Context(), proxy)
	require.ErrorContains(t, err, "boom")
}

func TestMCPRemoteProxyEnsureServiceMissingAuthConfig(t *testing.T) {
	t.Parallel()

	proxy := v1beta1test.NewMCPRemoteProxy("proxy", "default", v1beta1test.WithRemoteProxyExternalAuthConfigRef("missing"))
	reconciler, c := newTestMCPRemoteProxyReconciler(t, proxy)

	_, err := reconciler.ensureService(t.Context(), proxy)
	require.NoError(t, err)

	service := &corev1.Service{}
	require.NoError(t, c.Get(t.Context(), client.ObjectKey{Name: createProxyServiceName(proxy.Name), Namespace: "default"}, service))
	assert.Equal(t, expectedServicePorts(int32(proxy.GetProxyPort()), false), service.Spec.Ports)
}

// authConfigGetTrace records how many MCPExternalAuthConfig Gets a client has
// served and how many had been served when the first Service Get arrived.
type authConfigGetTrace struct {
	count            int
	beforeServiceGet int
}

// newClientFailingNthAuthConfigGet returns a fake client holding objs whose
// failAt-th MCPExternalAuthConfig Get fails with an error that is not NotFound.
// A failAt of 0 fails nothing, so a first run can read the trace.
func newClientFailingNthAuthConfigGet(t *testing.T, failAt int, objs ...client.Object) (client.Client, *authConfigGetTrace) {
	t.Helper()
	trace := &authConfigGetTrace{}
	c := fake.NewClientBuilder().WithScheme(testutil.NewScheme(t)).WithObjects(objs...).
		WithStatusSubresource(&mcpv1beta1.MCPServer{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				switch obj.(type) {
				case *mcpv1beta1.MCPExternalAuthConfig:
					trace.count++
					if trace.count == failAt {
						return errors.New("boom")
					}
				case *corev1.Service:
					if trace.beforeServiceGet == 0 {
						trace.beforeServiceGet = trace.count
					}
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	return c, trace
}

// The TLS listener resolver is the last MCPExternalAuthConfig Get before the
// Service Get. A first run finds its position, a second run fails exactly that Get.
func TestMCPServerReconcileAuthServerTLSListenerLookupFailure(t *testing.T) {
	t.Parallel()

	server := v1beta1test.NewMCPServer("server", "default",
		v1beta1test.WithImage("test"), v1beta1test.WithExternalAuthConfigRef("auth"),
		v1beta1test.WithOIDCConfigRef("oidc", "https://mcp.example.com"),
		v1beta1test.Mutate(func(m *mcpv1beta1.MCPServer) { m.Spec.OIDCConfigRef.ResourceURL = "https://mcp.example.com" }))
	authConfig := externalAuthConfigWithTLSListener(false)
	authConfig.Spec.EmbeddedAuthServer.Issuer = "https://auth.example.com"
	authConfig.Spec.EmbeddedAuthServer.UpstreamProviders = []mcpv1beta1.UpstreamProviderConfig{{
		Name:       "github",
		Type:       mcpv1beta1.UpstreamProviderTypeOIDC,
		OIDCConfig: &mcpv1beta1.OIDCUpstreamConfig{IssuerURL: "https://github.com", ClientID: "client-id"},
	}}
	oidcConfig := &mcpv1beta1.MCPOIDCConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "oidc", Namespace: "default"},
		Spec: mcpv1beta1.MCPOIDCConfigSpec{
			Type:   mcpv1beta1.MCPOIDCConfigTypeInline,
			Inline: &mcpv1beta1.InlineOIDCSharedConfig{Issuer: "https://auth.example.com"},
		},
		Status: mcpv1beta1.MCPOIDCConfigStatus{Conditions: []metav1.Condition{{
			Type: mcpv1beta1.ConditionTypeValid, Status: metav1.ConditionTrue, Reason: "ValidationSucceeded",
		}}},
	}
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(server)}
	serviceKey := client.ObjectKey{Name: ctrlutil.CreateProxyServiceName(server.Name), Namespace: "default"}
	// Early passes add the finalizer and create the Deployment, a later one
	// creates the Service.
	const passes = 5

	scheme := testutil.NewScheme(t)
	probe, trace := newClientFailingNthAuthConfigGet(t, 0, server.DeepCopy(), authConfig.DeepCopy(), oidcConfig.DeepCopy())
	probeReconciler := newTestMCPServerReconciler(probe, scheme, kubernetes.PlatformKubernetes)
	for range passes {
		_, err := probeReconciler.Reconcile(t.Context(), req)
		require.NoError(t, err)
	}
	require.NoError(t, probe.Get(t.Context(), serviceKey, &corev1.Service{}))
	require.NotZero(t, trace.beforeServiceGet)

	c, _ := newClientFailingNthAuthConfigGet(t, trace.beforeServiceGet, server, authConfig, oidcConfig)
	reconciler := newTestMCPServerReconciler(c, scheme, kubernetes.PlatformKubernetes)
	var err error
	for range passes {
		if _, err = reconciler.Reconcile(t.Context(), req); err != nil {
			break
		}
	}
	require.ErrorContains(t, err, "boom")
	assert.True(t, apierrors.IsNotFound(c.Get(t.Context(), serviceKey, &corev1.Service{})),
		"the Service must not be created after the lookup failed")
}
