// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8sptr "k8s.io/utils/ptr"

	authrunner "github.com/stacklok/toolhive/pkg/authserver/runner"
)

const (
	// ProxyHTTPPortName is the container and Service port name of the proxy's
	// main HTTP port.
	ProxyHTTPPortName = "http"

	// AuthServerTLSListenerPortName is the container and Service port name of the
	// embedded auth server's TLS listener.
	AuthServerTLSListenerPortName = "https-auth"
)

// ProxyOwnedPortNames returns the names of the ports the operator owns on the
// proxy Service. The slice is freshly allocated on every call.
func ProxyOwnedPortNames() []string {
	return []string{ProxyHTTPPortName, AuthServerTLSListenerPortName}
}

// ProxyContainerPorts returns the container ports the operator renders for a
// proxy: the HTTP port, plus the auth server TLS listener port when tlsListener
// is set.
func ProxyContainerPorts(port int32, tlsListener bool) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{{
		Name:          ProxyHTTPPortName,
		ContainerPort: port,
		Protocol:      corev1.ProtocolTCP,
	}}
	if tlsListener {
		ports = append(ports, corev1.ContainerPort{
			Name:          AuthServerTLSListenerPortName,
			ContainerPort: authrunner.TLSListenerPort,
			Protocol:      corev1.ProtocolTCP,
		})
	}
	return ports
}

// ProxyServicePorts returns the Service ports the operator renders for a
// proxy: the HTTP port, plus the auth server TLS listener port when tlsListener
// is set.
func ProxyServicePorts(port int32, tlsListener bool) []corev1.ServicePort {
	ports := []corev1.ServicePort{{
		Name:       ProxyHTTPPortName,
		Port:       port,
		TargetPort: intstr.FromInt32(port),
		Protocol:   corev1.ProtocolTCP,
	}}
	if tlsListener {
		ports = append(ports, corev1.ServicePort{
			Name:        AuthServerTLSListenerPortName,
			Port:        authrunner.TLSListenerPort,
			TargetPort:  intstr.FromInt32(authrunner.TLSListenerPort),
			Protocol:    corev1.ProtocolTCP,
			AppProtocol: k8sptr.To("https"),
		})
	}
	return ports
}

// ContainerPortsDrifted reports whether the operator-managed container ports
// are missing or wrong in current. Every desired port must be satisfied by some
// port in current with the same ContainerPort and Protocol, whatever its name;
// an empty Protocol is not defaulted. Ports are matched by number and protocol
// because existing Deployments may carry a port renamed by spec.podTemplateSpec
// (its merge key is containerPort), and a rename must not be reported as drift.
//
// owned lists every port the operator can render for the workload. A port in
// current that equals one of them exactly (same Name, ContainerPort and
// Protocol) but equals none of the desired ports is a leftover, and is drift.
// A port that only shares a name with an owned port, such as a user-added
// https-auth on another number, is not a leftover.
//
// Other extra ports and a different order do not by themselves trigger an
// update. Any update the operator makes replaces the pod template, so extra
// ports are removed at that point, except ports that a vMCP
// spec.podTemplateSpec adds, which are applied again on every update.
func ContainerPortsDrifted(current, desired, owned []corev1.ContainerPort) bool {
	for _, want := range desired {
		satisfied := slices.ContainsFunc(current, func(got corev1.ContainerPort) bool {
			return got.ContainerPort == want.ContainerPort && got.Protocol == want.Protocol
		})
		if !satisfied {
			return true
		}
	}
	for _, port := range current {
		if slices.Contains(owned, port) && !slices.Contains(desired, port) {
			return true
		}
	}
	return false
}

// ServicePortsDrifted reports whether the operator-managed ports are missing or
// wrong in current. Services are co-owned with external controllers, so extra
// ports, a different order, NodePort and an AppProtocol on a port where desired
// sets none do not by themselves trigger an update. A port in current whose
// name is in owned but is not desired does. Any update the operator makes
// replaces the whole port list, so extra ports are removed at that point.
func ServicePortsDrifted(current, desired []corev1.ServicePort, owned []string) bool {
	currentByName := make(map[string]corev1.ServicePort, len(current))
	for _, port := range current {
		currentByName[port.Name] = port
	}
	desiredNames := make(map[string]struct{}, len(desired))
	for _, want := range desired {
		desiredNames[want.Name] = struct{}{}
		got, ok := currentByName[want.Name]
		if !ok ||
			got.Port != want.Port ||
			got.TargetPort != want.TargetPort ||
			got.Protocol != want.Protocol ||
			(want.AppProtocol != nil && !k8sptr.Equal(got.AppProtocol, want.AppProtocol)) {
			return true
		}
	}
	for _, port := range current {
		if _, ok := desiredNames[port.Name]; !ok && slices.Contains(owned, port.Name) {
			return true
		}
	}
	return false
}
