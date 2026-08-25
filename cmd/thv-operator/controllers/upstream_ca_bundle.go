// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

const (
	// EmbeddedAuthCABundleConfigMapIndex indexes auth configs by referenced CA ConfigMap name.
	EmbeddedAuthCABundleConfigMapIndex = "toolhive.stacklok.dev/embedded-auth-ca-configmap"
	// EmbeddedAuthConfigIndex indexes workloads by referenced embedded auth config name.
	EmbeddedAuthConfigIndex = "toolhive.stacklok.dev/embedded-auth-config"
)

func providerCABundleConfigMapName(provider mcpv1beta1.UpstreamProviderConfig) string {
	ref := provider.CABundleRef()
	if ref == nil || ref.ConfigMapRef == nil {
		return ""
	}
	return ref.ConfigMapRef.Name
}

func caBundleKey(ref *mcpv1beta1.CABundleSource) string {
	if ref == nil || ref.ConfigMapRef == nil || ref.ConfigMapRef.Key == "" {
		return validation.OIDCCABundleDefaultKey
	}
	return ref.ConfigMapRef.Key
}

// IndexEmbeddedAuthCABundleConfigMaps returns the ConfigMap names referenced by
// an MCPExternalAuthConfig's embedded-auth upstream CA bundles.
func IndexEmbeddedAuthCABundleConfigMaps(obj client.Object) []string {
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
