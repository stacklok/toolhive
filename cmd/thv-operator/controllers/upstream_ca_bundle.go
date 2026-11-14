// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
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
