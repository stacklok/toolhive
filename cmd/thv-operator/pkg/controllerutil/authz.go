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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	"github.com/stacklok/toolhive/pkg/runner"
)

const (
	// DefaultAuthzKey is the default key for authorization policies in ConfigMaps
	DefaultAuthzKey = "authz.json"
)

// GenerateAuthzVolumeConfig generates volume mount and volume for authorization policies
func GenerateAuthzVolumeConfig(
	authzConfig *mcpv1alpha1.AuthzConfigRef,
	resourceName string,
) (*corev1.VolumeMount, *corev1.Volume) {
	if authzConfig == nil {
		return nil, nil
	}

	switch authzConfig.Type {
	case mcpv1alpha1.AuthzConfigTypeConfigMap:
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

	case mcpv1alpha1.AuthzConfigTypeInline:
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
	authzConfig *mcpv1alpha1.AuthzConfigRef,
	labels map[string]string,
) error {
	if authzConfig == nil || authzConfig.Type != mcpv1alpha1.AuthzConfigTypeInline ||
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
	authzRef *mcpv1alpha1.AuthzConfigRef,
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

// AddAuthzConfigOptions adds authorization configuration options to builder options
func AddAuthzConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	authzRef *mcpv1alpha1.AuthzConfigRef,
	options *[]runner.RunConfigBuilderOption,
) error {
	if authzRef == nil {
		return nil
	}

	switch authzRef.Type {
	case mcpv1alpha1.AuthzConfigTypeInline:
		return addAuthzInlineConfigOptions(authzRef, options)

	case mcpv1alpha1.AuthzConfigTypeConfigMap:
		// Validate reference
		if authzRef.ConfigMap == nil || authzRef.ConfigMap.Name == "" {
			return fmt.Errorf("configMap authz config type specified but reference is missing name")
		}
		key := authzRef.ConfigMap.Key
		if key == "" {
			key = DefaultAuthzKey
		}

		// Ensure we have a Kubernetes client to fetch the ConfigMap
		if c == nil {
			return fmt.Errorf("kubernetes client is not configured for ConfigMap authz resolution")
		}

		// Fetch the ConfigMap
		var cm corev1.ConfigMap
		if err := c.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      authzRef.ConfigMap.Name,
		}, &cm); err != nil {
			return fmt.Errorf("failed to get Authz ConfigMap %s/%s: %w", namespace, authzRef.ConfigMap.Name, err)
		}

		raw, ok := cm.Data[key]
		if !ok {
			return fmt.Errorf("authz ConfigMap %s/%s is missing key %q", namespace, authzRef.ConfigMap.Name, key)
		}
		if len(strings.TrimSpace(raw)) == 0 {
			return fmt.Errorf("authz ConfigMap %s/%s key %q is empty", namespace, authzRef.ConfigMap.Name, key)
		}

		// Unmarshal into authz.Config supporting YAML or JSON
		var cfg authz.Config
		// Try YAML first (it also handles JSON)
		if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
			// Fallback to JSON explicitly for clearer error paths
			if err2 := json.Unmarshal([]byte(raw), &cfg); err2 != nil {
				return fmt.Errorf("failed to parse authz config from ConfigMap %s/%s key %q: %w; json fallback error: %w",
					namespace, authzRef.ConfigMap.Name, key, err, err2)
			}
		}

		// Validate the config
		if err := cfg.Validate(); err != nil {
			return fmt.Errorf("invalid authz config from ConfigMap %s/%s key %q: %w",
				namespace, authzRef.ConfigMap.Name, key, err)
		}

		*options = append(*options, runner.WithAuthzConfig(&cfg))
		return nil

	default:
		// Unknown type
		return fmt.Errorf("unknown authz config type: %s", authzRef.Type)
	}
}

// GetAuthzConfigForWorkload fetches the MCPAuthzConfig referenced by a workload.
// Returns nil if the ref is nil.
func GetAuthzConfigForWorkload(
	ctx context.Context,
	c client.Client,
	namespace string,
	ref *mcpv1alpha1.MCPAuthzConfigReference,
) (*mcpv1alpha1.MCPAuthzConfig, error) {
	if ref == nil {
		return nil, nil
	}

	authzConfig := &mcpv1alpha1.MCPAuthzConfig{}
	if err := c.Get(ctx, types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}, authzConfig); err != nil {
		return nil, fmt.Errorf("failed to get MCPAuthzConfig %s/%s: %w", namespace, ref.Name, err)
	}

	return authzConfig, nil
}

// ValidateAuthzConfigReady checks that the MCPAuthzConfig has a Valid=True condition.
// Returns an error if the config is not ready.
func ValidateAuthzConfigReady(authzConfig *mcpv1alpha1.MCPAuthzConfig) error {
	validCondition := meta.FindStatusCondition(authzConfig.Status.Conditions, mcpv1alpha1.ConditionTypeAuthzConfigValid)
	if validCondition == nil || validCondition.Status != metav1.ConditionTrue {
		msg := fmt.Sprintf("MCPAuthzConfig %s is not valid", authzConfig.Name)
		if validCondition != nil {
			msg = fmt.Sprintf("MCPAuthzConfig %s is not valid: %s", authzConfig.Name, validCondition.Message)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// GenerateAuthzVolumeConfigFromRef generates volume mount and volume for an MCPAuthzConfig
// reference. It creates volumes that mount a ConfigMap named "{resourceName}-authzref" containing
// the reconstructed full authorization config at /etc/toolhive/authz/authz.json.
func GenerateAuthzVolumeConfigFromRef(resourceName string) (*corev1.VolumeMount, *corev1.Volume) {
	configMapName := fmt.Sprintf("%s-authzref", resourceName)

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
					Name: configMapName,
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
}

// EnsureAuthzConfigMapFromRef creates or updates a ConfigMap containing the reconstructed
// full authorization config JSON from an MCPAuthzConfig reference.
func EnsureAuthzConfigMapFromRef(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	namespace string,
	resourceName string,
	authzConfig *mcpv1alpha1.MCPAuthzConfig,
	labels map[string]string,
) error {
	fullConfigJSON, err := BuildFullAuthzConfigJSON(authzConfig.Spec)
	if err != nil {
		return fmt.Errorf("failed to build full authz config JSON: %w", err)
	}

	configMapName := fmt.Sprintf("%s-authzref", resourceName)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Labels:    labels,
		},
		Data: map[string]string{
			DefaultAuthzKey: string(fullConfigJSON),
		},
	}

	configMapsClient := configmaps.NewClient(c, scheme)
	if _, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, owner); err != nil {
		return fmt.Errorf("failed to upsert authzref ConfigMap: %w", err)
	}

	return nil
}

// BuildFullAuthzConfigJSON reconstructs the full authorizer config JSON from a
// MCPAuthzConfig spec. Extracted here so both the MCPAuthzConfig controller and
// workload controllers can use it.
func BuildFullAuthzConfigJSON(spec mcpv1alpha1.MCPAuthzConfigSpec) ([]byte, error) {
	factory := authorizers.GetFactory(spec.Type)
	if factory == nil {
		return nil, fmt.Errorf("unsupported authorizer type: %s (registered types: %v)",
			spec.Type, authorizers.RegisteredTypes())
	}

	configKey := factory.ConfigKey()

	fullConfig := map[string]json.RawMessage{
		"version": mustMarshalJSON(authzConfigVersion),
		"type":    mustMarshalJSON(spec.Type),
		configKey: spec.Config.Raw,
	}

	result, err := json.Marshal(fullConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal full authz config: %w", err)
	}
	return result, nil
}

const authzConfigVersion = "1.0"

func mustMarshalJSON(v string) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal %q: %v", v, err))
	}
	return b
}

// AddAuthzConfigRefOptions resolves an MCPAuthzConfig reference and adds the
// authorization configuration to builder options.
func AddAuthzConfigRefOptions(
	authzConfig *mcpv1alpha1.MCPAuthzConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	fullConfigJSON, err := BuildFullAuthzConfigJSON(authzConfig.Spec)
	if err != nil {
		return fmt.Errorf("failed to build full authz config: %w", err)
	}

	var cfg authz.Config
	if err := json.Unmarshal(fullConfigJSON, &cfg); err != nil {
		return fmt.Errorf("failed to parse authz config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid authz config from MCPAuthzConfig %s: %w", authzConfig.Name, err)
	}

	*options = append(*options, runner.WithAuthzConfig(&cfg))
	return nil
}
