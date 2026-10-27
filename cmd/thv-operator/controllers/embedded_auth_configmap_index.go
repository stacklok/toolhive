// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

const (
	// EmbeddedAuthConfigMapRefIndex indexes auth configs by referenced ConfigMap name.
	EmbeddedAuthConfigMapRefIndex = "toolhive.stacklok.dev/embedded-auth-ca-configmap"
	// EmbeddedAuthConfigIndex indexes workloads by referenced embedded auth config name.
	EmbeddedAuthConfigIndex = "toolhive.stacklok.dev/embedded-auth-config"
)

// IndexEmbeddedAuthConfigMapRefs returns the ConfigMap names referenced by
// an MCPExternalAuthConfig's embedded-auth CA bundles.
func IndexEmbeddedAuthConfigMapRefs(obj client.Object) []string {
	config, ok := obj.(*mcpv1beta1.MCPExternalAuthConfig)
	if !ok || config.Spec.EmbeddedAuthServer == nil {
		return nil
	}
	keys := make([]string, 0)
	for _, provider := range config.Spec.EmbeddedAuthServer.UpstreamProviders {
		if name := providerCABundleConfigMapName(provider); name != "" {
			keys = append(keys, name)
		}
	}
	for i := range config.Spec.EmbeddedAuthServer.TrustedIssuers {
		ref := config.Spec.EmbeddedAuthServer.TrustedIssuers[i].CABundleRef
		if ref != nil && ref.ConfigMapRef != nil && ref.ConfigMapRef.Name != "" {
			keys = append(keys, ref.ConfigMapRef.Name)
		}
	}
	return keys
}

func indexMCPServerEmbeddedAuthConfig(obj client.Object) []string {
	server, ok := obj.(*mcpv1beta1.MCPServer)
	if !ok {
		return nil
	}
	if name := ctrlutil.EmbeddedAuthServerConfigName(server.Spec.ExternalAuthConfigRef, server.Spec.AuthServerRef); name != "" {
		return []string{name}
	}
	return nil
}

func indexMCPRemoteProxyEmbeddedAuthConfig(obj client.Object) []string {
	proxy, ok := obj.(*mcpv1beta1.MCPRemoteProxy)
	if !ok {
		return nil
	}
	if name := ctrlutil.EmbeddedAuthServerConfigName(proxy.Spec.ExternalAuthConfigRef, proxy.Spec.AuthServerRef); name != "" {
		return []string{name}
	}
	return nil
}
