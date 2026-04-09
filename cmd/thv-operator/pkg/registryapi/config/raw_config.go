// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
)

// RawConfigToConfigMap creates a ConfigMap from a raw YAML config string
// without parsing or transforming its content. It applies the same content
// checksum annotation used by ToConfigMapWithContentChecksum.
func RawConfigToConfigMap(registryName, namespace, configYAML string) (*corev1.ConfigMap, error) {
	if registryName == "" {
		return nil, fmt.Errorf("registry name is required")
	}
	if configYAML == "" {
		return nil, fmt.Errorf("config YAML is required")
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-registry-server-config", registryName),
			Namespace: namespace,
			Annotations: map[string]string{
				checksum.ContentChecksumAnnotation: ctrlutil.CalculateConfigHash([]byte(configYAML)),
			},
		},
		Data: map[string]string{
			RegistryServerConfigFileName: configYAML,
		},
	}, nil
}
