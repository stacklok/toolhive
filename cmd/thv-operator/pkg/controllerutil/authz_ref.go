// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/authz"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/runner"
)

// BuildFullAuthzConfigJSON reconstructs the full authorizer config JSON from a
// MCPAuthzConfig spec and returns it alongside the resolved factory. The JSON
// shape is the one accepted by authz.Config / authorizers.Config and stored in
// ConfigMaps: {"version": "1.0", "type": "<type>", "<configKey>": {<config>}}.
// It is backend-agnostic — the per-backend key is supplied by the factory's
// ConfigKey() (e.g. "cedar" for cedarv1, "pdp" for httpv1) — so both the
// MCPAuthzConfig controller and the workload controllers can use it without
// special-casing the backend. Returning the factory together with the JSON lets
// callers skip a second registry lookup when they also need to validate.
func BuildFullAuthzConfigJSON(spec mcpv1beta1.MCPAuthzConfigSpec) ([]byte, authorizers.AuthorizerFactory, error) {
	factory := authorizers.GetFactory(spec.Type)
	if factory == nil {
		return nil, nil, fmt.Errorf("unsupported authorizer type: %s (registered types: %v)",
			spec.Type, authorizers.RegisteredTypes())
	}

	if len(spec.Config.Raw) == 0 {
		return nil, nil, fmt.Errorf("config field is empty")
	}

	// Marshaling a string constant and a plain string field cannot fail.
	versionJSON, _ := json.Marshal(AuthzConfigVersion)
	typeJSON, _ := json.Marshal(spec.Type)
	fullConfig := map[string]json.RawMessage{
		"version":           versionJSON,
		"type":              typeJSON,
		factory.ConfigKey(): spec.Config.Raw,
	}

	result, err := json.Marshal(fullConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal full authz config: %w", err)
	}
	return result, factory, nil
}

// GetAuthzConfigForWorkload fetches the MCPAuthzConfig referenced by ref in the
// given namespace. Returns (nil, nil) when ref is nil so callers can invoke it
// unconditionally. Mirrors GetOIDCConfigForServer.
func GetAuthzConfigForWorkload(
	ctx context.Context,
	c client.Client,
	namespace string,
	ref *mcpv1beta1.MCPAuthzConfigReference,
) (*mcpv1beta1.MCPAuthzConfig, error) {
	if ref == nil {
		return nil, nil
	}
	var authzConfig mcpv1beta1.MCPAuthzConfig
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &authzConfig); err != nil {
		return nil, fmt.Errorf("failed to get MCPAuthzConfig %s/%s: %w", namespace, ref.Name, err)
	}
	return &authzConfig, nil
}

// ValidateAuthzConfigReady returns an error unless the referenced config's
// ConditionTypeAuthzConfigValid condition is True — i.e. the MCPAuthzConfig
// controller has validated the spec. A workload must not consume a config that
// its owning controller has flagged invalid.
func ValidateAuthzConfigReady(authzConfig *mcpv1beta1.MCPAuthzConfig) error {
	if authzConfig == nil {
		return fmt.Errorf("authz config is nil")
	}
	if !meta.IsStatusConditionTrue(authzConfig.Status.Conditions, mcpv1beta1.ConditionTypeAuthzConfigValid) {
		return fmt.Errorf("MCPAuthzConfig %s/%s is not valid (condition %q is not True)",
			authzConfig.Namespace, authzConfig.Name, mcpv1beta1.ConditionTypeAuthzConfigValid)
	}
	return nil
}

// BuildAuthzConfigFromRef builds a validated *authz.Config from a referenced
// MCPAuthzConfig, backend-agnostically (cedarv1, httpv1, ...). The resulting
// config is safe to embed into a RunConfig via runner.WithAuthzConfig, or to
// unwrap into backend-specific options (e.g. via ExtractCedarAuthzOptions for
// the Cedar-only VirtualMCPServer converter path).
func BuildAuthzConfigFromRef(authzConfig *mcpv1beta1.MCPAuthzConfig) (*authz.Config, error) {
	data, _, err := BuildFullAuthzConfigJSON(authzConfig.Spec)
	if err != nil {
		return nil, err
	}
	var cfg authz.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse authz config from MCPAuthzConfig %s/%s: %w",
			authzConfig.Namespace, authzConfig.Name, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid authz config from MCPAuthzConfig %s/%s: %w",
			authzConfig.Namespace, authzConfig.Name, err)
	}
	return &cfg, nil
}

// AddAuthzConfigRefOptions resolves the referenced MCPAuthzConfig into an
// authz.Config (any registered backend) and appends runner.WithAuthzConfig.
// No-op when ref is nil. Parallel to AddAuthzConfigOptions for the inline
// spec.authzConfig path. The referenced config must exist and be Valid.
func AddAuthzConfigRefOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	ref *mcpv1beta1.MCPAuthzConfigReference,
	options *[]runner.RunConfigBuilderOption,
) error {
	if ref == nil {
		return nil
	}
	authzConfig, err := GetAuthzConfigForWorkload(ctx, c, namespace, ref)
	if err != nil {
		return err
	}
	if err := ValidateAuthzConfigReady(authzConfig); err != nil {
		return err
	}
	cfg, err := BuildAuthzConfigFromRef(authzConfig)
	if err != nil {
		return err
	}
	*options = append(*options, runner.WithAuthzConfig(cfg))
	return nil
}
