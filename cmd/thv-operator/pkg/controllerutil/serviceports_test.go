// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

var testOwnedPorts = []string{"http", "https-auth"}

func TestServicePortsDrifted(t *testing.T) {
	t.Parallel()

	h2c := "kubernetes.io/h2c"
	httpsAppProtocol := "https"
	httpPort := corev1.ServicePort{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP}
	httpWith := func(mutate func(*corev1.ServicePort)) corev1.ServicePort {
		port := httpPort
		mutate(&port)
		return port
	}
	tlsPort := corev1.ServicePort{
		Name:        "https-auth",
		Port:        8443,
		TargetPort:  intstr.FromInt32(8443),
		Protocol:    corev1.ProtocolTCP,
		AppProtocol: &httpsAppProtocol,
	}
	tlsWith := func(mutate func(*corev1.ServicePort)) corev1.ServicePort {
		port := tlsPort
		mutate(&port)
		return port
	}
	metrics := corev1.ServicePort{Name: "metrics", Port: 9090, TargetPort: intstr.FromInt32(9090), Protocol: corev1.ProtocolTCP}
	unowned := corev1.ServicePort{Name: "https-auth-x", Port: 9443, TargetPort: intstr.FromInt32(9443), Protocol: corev1.ProtocolTCP}

	tests := []struct {
		name    string
		current []corev1.ServicePort
		desired []corev1.ServicePort
		want    bool
	}{
		{"equal", []corev1.ServicePort{httpPort}, []corev1.ServicePort{httpPort}, false},
		{"missing http", nil, []corev1.ServicePort{httpPort}, true},
		{"port differs", []corev1.ServicePort{httpWith(func(p *corev1.ServicePort) { p.Port = 8081 })}, []corev1.ServicePort{httpPort}, true},
		{"target port differs", []corev1.ServicePort{httpWith(func(p *corev1.ServicePort) { p.TargetPort = intstr.FromInt32(8081) })}, []corev1.ServicePort{httpPort}, true},
		{"protocol differs", []corev1.ServicePort{httpWith(func(p *corev1.ServicePort) { p.Protocol = corev1.ProtocolUDP })}, []corev1.ServicePort{httpPort}, true},
		{"extra port", []corev1.ServicePort{httpPort, metrics}, []corev1.ServicePort{httpPort}, false},
		{"user appProtocol on http", []corev1.ServicePort{httpWith(func(p *corev1.ServicePort) { p.AppProtocol = &h2c })}, []corev1.ServicePort{httpPort}, false},
		{"desired appProtocol differs", []corev1.ServicePort{httpPort, tlsWith(func(p *corev1.ServicePort) { p.AppProtocol = &h2c })}, []corev1.ServicePort{httpPort, tlsPort}, true},
		{"desired appProtocol missing", []corev1.ServicePort{httpPort, tlsWith(func(p *corev1.ServicePort) { p.AppProtocol = nil })}, []corev1.ServicePort{httpPort, tlsPort}, true},
		{"leftover https-auth", []corev1.ServicePort{httpPort, tlsPort}, []corev1.ServicePort{httpPort}, true},
		{"missing https-auth", []corev1.ServicePort{httpPort}, []corev1.ServicePort{httpPort, tlsPort}, true},
		{"reordered", []corev1.ServicePort{tlsPort, metrics, httpPort}, []corev1.ServicePort{httpPort, tlsPort}, false},
		{"node port set", []corev1.ServicePort{httpWith(func(p *corev1.ServicePort) { p.NodePort = 30080 })}, []corev1.ServicePort{httpPort}, false},
		{"unowned name not desired is not drift", []corev1.ServicePort{httpPort, unowned}, []corev1.ServicePort{httpPort}, false},
		{"owned name not desired is drift", []corev1.ServicePort{tlsPort}, nil, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, ServicePortsDrifted(test.current, test.desired, testOwnedPorts))
		})
	}
}

func TestContainerPortsDrifted(t *testing.T) {
	t.Parallel()

	httpPort := corev1.ContainerPort{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}
	tlsPort := corev1.ContainerPort{Name: "https-auth", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}
	metrics := corev1.ContainerPort{Name: "metrics", ContainerPort: 9090, Protocol: corev1.ProtocolTCP}

	tests := []struct {
		name    string
		current []corev1.ContainerPort
		desired []corev1.ContainerPort
		want    bool
	}{
		{"equal", []corev1.ContainerPort{httpPort}, []corev1.ContainerPort{httpPort}, false},
		{"missing http", nil, []corev1.ContainerPort{httpPort}, true},
		{"wrong number", []corev1.ContainerPort{{Name: "http", ContainerPort: 8081, Protocol: corev1.ProtocolTCP}}, []corev1.ContainerPort{httpPort}, true},
		{"wrong protocol", []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolUDP}}, []corev1.ContainerPort{httpPort}, true},
		{"extra port before http", []corev1.ContainerPort{metrics, httpPort}, []corev1.ContainerPort{httpPort}, false},
		{"extra port after http", []corev1.ContainerPort{httpPort, metrics}, []corev1.ContainerPort{httpPort}, false},
		{"leftover https-auth", []corev1.ContainerPort{httpPort, tlsPort}, []corev1.ContainerPort{httpPort}, true},
		{"missing https-auth", []corev1.ContainerPort{httpPort}, []corev1.ContainerPort{httpPort, tlsPort}, true},
		{"http renamed but same number and protocol is not drift", []corev1.ContainerPort{{Name: "x", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}, []corev1.ContainerPort{httpPort}, false},
		{"https-auth renamed to https on 8443 with the listener on is not drift", []corev1.ContainerPort{httpPort, {Name: "https", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}}, []corev1.ContainerPort{httpPort, tlsPort}, false},
		{"listener off and a port named https on 8443 is not drift", []corev1.ContainerPort{httpPort, {Name: "https", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}}, []corev1.ContainerPort{httpPort}, false},
		{"unnamed port on the wanted number and protocol is not drift", []corev1.ContainerPort{{ContainerPort: 8080, Protocol: corev1.ProtocolTCP}}, []corev1.ContainerPort{httpPort}, false},
		{"empty protocol vs TCP", []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, []corev1.ContainerPort{httpPort}, true},
		{"TCP and UDP on the same number is not drift", []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolUDP}, httpPort}, []corev1.ContainerPort{httpPort}, false},
		{"stale https-auth is not drift when https-auth is desired and 8443 is served as https", []corev1.ContainerPort{httpPort, {Name: "https", ContainerPort: 8443, Protocol: corev1.ProtocolTCP}, {Name: "https-auth", ContainerPort: 9443, Protocol: corev1.ProtocolTCP}}, []corev1.ContainerPort{httpPort, tlsPort}, false},
		{"reordered", []corev1.ContainerPort{tlsPort, metrics, httpPort}, []corev1.ContainerPort{httpPort, tlsPort}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.want, ContainerPortsDrifted(test.current, test.desired, testOwnedPorts))
		})
	}
}

func TestProxyPorts(t *testing.T) {
	t.Parallel()

	httpsAppProtocol := "https"
	tests := []struct {
		name          string
		tlsListener   bool
		wantContainer []corev1.ContainerPort
		wantService   []corev1.ServicePort
	}{
		{
			name:          "tls listener off",
			wantContainer: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP}},
			wantService: []corev1.ServicePort{
				{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
			},
		},
		{
			name:        "tls listener on",
			tlsListener: true,
			wantContainer: []corev1.ContainerPort{
				{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
				{Name: "https-auth", ContainerPort: 8443, Protocol: corev1.ProtocolTCP},
			},
			wantService: []corev1.ServicePort{
				{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
				{
					Name:        "https-auth",
					Port:        8443,
					TargetPort:  intstr.FromInt32(8443),
					Protocol:    corev1.ProtocolTCP,
					AppProtocol: &httpsAppProtocol,
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, test.wantContainer, ProxyContainerPorts(8080, test.tlsListener))
			assert.Equal(t, test.wantService, ProxyServicePorts(8080, test.tlsListener))
		})
	}
}

func TestProxyOwnedPortNames(t *testing.T) {
	t.Parallel()

	got := ProxyOwnedPortNames()
	assert.Equal(t, []string{"http", "https-auth"}, got)

	got[0] = "mutated"
	assert.Equal(t, []string{"http", "https-auth"}, ProxyOwnedPortNames())
}
