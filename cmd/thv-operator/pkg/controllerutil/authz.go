// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package controllerutil provides utility functions for the ToolHive Kubernetes operator controllers.
package controllerutil

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// DefaultAuthzKey is the default key for authorization policies in ConfigMaps
	DefaultAuthzKey = "authz.json"
)

// GenerateAuthzVolumeConfig generates volume mount and volume for authorization policies
func GenerateAuthzVolumeConfig(
	authzConfig *mcpv1beta1.AuthzConfigRef,
	resourceName string,
) (*corev1.VolumeMount, *corev1.Volume) {
	if authzConfig == nil {
		return nil, nil
	}

	switch authzConfig.Type {
	case mcpv1beta1.AuthzConfigTypeConfigMap:
		if authzConfig.ConfigMap == nil {
			return nil, nil
		}

		volumeMount := &corev1.VolumeMount{
			Name:      "authz-config",
			MountPath: "/etc/toolhive/authz",
			ReadOnly:  true,
		}

		volume := &corev1.Volume{
			Name: "authz-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: authzConfig.ConfigMap.Name,
					},
					Items: []corev1.KeyToPath{
						{
							Key: func() string {
								if authzConfig.ConfigMap.Key != "" {
									return authzConfig.ConfigMap.Key
								}
								return DefaultAuthzKey
							}(),
							Path: DefaultAuthzKey,
						},
					},
				},
			},
		}

		return volumeMount, volume

	case mcpv1beta1.AuthzConfigTypeInline:
		if authzConfig.Inline == nil {
			return nil, nil
		}

		volumeMount := &corev1.VolumeMount{
			Name:      "authz-config",
			MountPath: "/etc/toolhive/authz",
			ReadOnly:  true,
		}

		volume := &corev1.Volume{
			Name: "authz-config",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-authz-inline", resourceName),
					},
					Items: []corev1.KeyToPath{
						{
							Key:  DefaultAuthzKey,
							Path: DefaultAuthzKey,
						},
					},
				},
			},
		}

		return volumeMount, volume

	default:
		return nil, nil
	}
}

// EnsureAuthzConfigMap ensures the authorization ConfigMap exists for inline configuration
func EnsureAuthzConfigMap(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	namespace string,
	resourceName string,
	authzConfig *mcpv1beta1.AuthzConfigRef,
	labels map[string]string,
) error {
	if authzConfig == nil || authzConfig.Type != mcpv1beta1.AuthzConfigTypeInline ||
		authzConfig.Inline == nil {
		return nil
	}

	configMapName := fmt.Sprintf("%s-authz-inline", resourceName)

	authzConfigData := map[string]interface{}{
		"version": "1.0",
		"type":    "cedarv1",
		"cedar": map[string]interface{}{
			"policies": authzConfig.Inline.Policies,
			"entities_json": func() string {
				if authzConfig.Inline.EntitiesJSON != "" {
					return authzConfig.Inline.EntitiesJSON
				}
				return "[]"
			}(),
		},
	}

	authzConfigJSON, err := json.Marshal(authzConfigData)
	if err != nil {
		return fmt.Errorf("failed to marshal inline authz config: %w", err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			DefaultAuthzKey: string(authzConfigJSON),
		},
	}

	// Use the kubernetes configmaps client for upsert operations
	configMapsClient := configmaps.NewClient(c, scheme)
	if _, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, owner); err != nil {
		return fmt.Errorf("failed to upsert authorization ConfigMap: %w", err)
	}

	return nil
}

func addAuthzInlineConfigOptions(
	authzRef *mcpv1beta1.AuthzConfigRef,
	options *[]runner.RunConfigBuilderOption,
) error {
	if authzRef.Inline == nil {
		return fmt.Errorf("inline authz config type specified but inline config is nil")
	}

	policies := authzRef.Inline.Policies
	entitiesJSON := authzRef.Inline.EntitiesJSON

	// Create authorization config using the full config structure
	// This maintains backwards compatibility with the v1.0 schema
	authzCfg, err := authz.NewConfig(cedar.Config{
		Version: "v1",
		Type:    cedar.ConfigType,
		Options: &cedar.ConfigOptions{
			Policies:     policies,
			EntitiesJSON: entitiesJSON,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create authz config: %w", err)
	}

	// Add authorization config to options
	*options = append(*options, runner.WithAuthzConfig(authzCfg))
	return nil
}

// LoadAuthzConfigFromConfigMap fetches the ConfigMap referenced by authzRef, parses its
// payload as an authz.Config (YAML or JSON), and validates the result. It is the shared
// resolver used by both the MCPServer/MCPRemoteProxy runner path (via AddAuthzConfigOptions)
// and the VirtualMCPServer converter.
//
// Failure modes (all returned as errors, never silently succeed):
//   - authzRef nil or not of type "configMap"
//   - ConfigMap reference missing name
//   - kubernetes client not configured
//   - ConfigMap not found, missing key, empty value, or malformed payload
//   - authz.Config fails validation
//
// The returned *authz.Config is safe to embed directly into RunConfig (via
// runner.WithAuthzConfig) or to read field-by-field for the vMCP converter.
func LoadAuthzConfigFromConfigMap(
	ctx context.Context,
	c client.Client,
	namespace string,
	authzRef *mcpv1beta1.AuthzConfigRef,
) (*authz.Config, error) {
	if authzRef == nil || authzRef.Type != mcpv1beta1.AuthzConfigTypeConfigMap {
		return nil, fmt.Errorf("authzRef is not of type %q", mcpv1beta1.AuthzConfigTypeConfigMap)
	}
	if authzRef.ConfigMap == nil || authzRef.ConfigMap.Name == "" {
		return nil, fmt.Errorf("configMap authz config type specified but reference is missing name")
	}
	if c == nil {
		return nil, fmt.Errorf("kubernetes client is not configured for ConfigMap authz resolution")
	}

	key := authzRef.ConfigMap.Key
	if key == "" {
		key = DefaultAuthzKey
	}

	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      authzRef.ConfigMap.Name,
	}, &cm); err != nil {
		return nil, fmt.Errorf("failed to get Authz ConfigMap %s/%s: %w", namespace, authzRef.ConfigMap.Name, err)
	}

	raw, ok := cm.Data[key]
	if !ok {
		return nil, fmt.Errorf("authz ConfigMap %s/%s is missing key %q", namespace, authzRef.ConfigMap.Name, key)
	}
	if len(strings.TrimSpace(raw)) == 0 {
		return nil, fmt.Errorf("authz ConfigMap %s/%s key %q is empty", namespace, authzRef.ConfigMap.Name, key)
	}

	// YAML unmarshal also handles JSON; the explicit JSON fallback gives a clearer error
	// message when both parsers reject the payload.
	var cfg authz.Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		if err2 := json.Unmarshal([]byte(raw), &cfg); err2 != nil {
			return nil, fmt.Errorf("failed to parse authz config from ConfigMap %s/%s key %q: %w; json fallback error: %w",
				namespace, authzRef.ConfigMap.Name, key, err, err2)
		}
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid authz config from ConfigMap %s/%s key %q: %w",
			namespace, authzRef.ConfigMap.Name, key, err)
	}

	return &cfg, nil
}

// AddAuthzConfigOptions adds authorization configuration options to builder options
func AddAuthzConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	authzRef *mcpv1beta1.AuthzConfigRef,
	options *[]runner.RunConfigBuilderOption,
) error {
	if authzRef == nil {
		return nil
	}

	switch authzRef.Type {
	case mcpv1beta1.AuthzConfigTypeInline:
		return addAuthzInlineConfigOptions(authzRef, options)

	case mcpv1beta1.AuthzConfigTypeConfigMap:
		cfg, err := LoadAuthzConfigFromConfigMap(ctx, c, namespace, authzRef)
		if err != nil {
			return err
		}
		*options = append(*options, runner.WithAuthzConfig(cfg))
		return nil

	default:
		return fmt.Errorf("unknown authz config type: %s", authzRef.Type)
	}
}
