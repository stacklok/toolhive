// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllerutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/externalauthsupport"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	exchange "github.com/stacklok/toolhive/pkg/oauthproto/tokenexchange"
	"github.com/stacklok/toolhive/pkg/runner"
)

// GenerateTokenExchangeEnvVars generates environment variables for token exchange
func GenerateTokenExchangeEnvVars(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	getExternalAuthConfig func(context.Context, client.Client, string, string) (*mcpv1beta1.MCPExternalAuthConfig, error),
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar

	if externalAuthConfigRef == nil {
		return envVars, nil
	}

	externalAuthConfig, err := getExternalAuthConfig(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig == nil {
		return nil, fmt.Errorf("MCPExternalAuthConfig %s not found", externalAuthConfigRef.Name)
	}

	if externalAuthConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeTokenExchange {
		return envVars, nil
	}

	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return envVars, nil
	}

	// Only add client secret env var if ClientSecretRef is provided
	if tokenExchangeSpec.ClientSecretRef != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: "TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: tokenExchangeSpec.ClientSecretRef.Name,
					},
					Key: tokenExchangeSpec.ClientSecretRef.Key,
				},
			},
		})
	}

	return envVars, nil
}

// AddExternalAuthConfigOptions adds external authentication configuration options to builder options
// This creates token exchange configuration which will be automatically converted to middleware by
// PopulateMiddlewareConfigs() when the runner starts. This ensures correct middleware ordering.
//
// The consumer parameter identifies the resource kind building the run config.
// The externalauthsupport matrix is enforced here, at the one dispatch point
// both MCPServer and MCPRemoteProxy route through, so a type the consumer does
// not accept results in an UnsupportedTypeError instead of a silent no-op
// (#5930). The consumer controllers validate earlier and surface the failure
// as a status condition; this guard keeps the run-config path honest even for
// a caller that skips that step.
//
// The oidcConfig parameter is used for embedded auth server configuration to populate:
//   - AllowedAudiences from oidcConfig.ResourceURL
//   - ScopesSupported from oidcConfig.Scopes
//
// For embedded auth server type, oidcConfig is REQUIRED and must have ResourceURL set.
func AddExternalAuthConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	mcpServerName string,
	consumer externalauthsupport.Consumer,
	externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	oidcConfig *oidc.OIDCConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	if externalAuthConfigRef == nil {
		return nil
	}

	// Fetch the MCPExternalAuthConfig
	externalAuthConfig, err := GetExternalAuthConfigByName(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if err := externalauthsupport.Validate(consumer, externalAuthConfig.Spec.Type); err != nil {
		return err
	}

	// Handle different auth types
	switch externalAuthConfig.Spec.Type {
	case mcpv1beta1.ExternalAuthTypeTokenExchange:
		return addTokenExchangeConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeBearerToken:
		return addBearerTokenConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeUnauthenticated:
		// No config to add for unauthenticated
		return nil
	case mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer:
		return AddEmbeddedAuthServerConfigOptions(ctx, c, namespace, mcpServerName, externalAuthConfigRef, oidcConfig, options)
	case mcpv1beta1.ExternalAuthTypeAWSSts:
		return addAWSStsConfig(externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeOBO:
		// The consuming reconcilers reject this earlier with a status
		// condition (ValidateOBOIncomingAuth); this guard keeps a RunConfig
		// from ever being emitted for a proxy with no user token to exchange.
		if err := ValidateOBOIncomingAuth(externalAuthConfig, oidcConfig != nil); err != nil {
			return err
		}
		return OBOApplyRunConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1beta1.ExternalAuthTypeHeaderInjection,
		mcpv1beta1.ExternalAuthTypeUpstreamInject,
		mcpv1beta1.ExternalAuthTypeXAA:
		// Nothing to add to the run config. The proxy runner does not apply
		// these types: a VirtualMCPServer that discovers the reference on this
		// backend applies them when it calls the backend.
		return nil
	default:
		return fmt.Errorf("unknown external auth type: %s", externalAuthConfig.Spec.Type)
	}
}

func addTokenExchangeConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return fmt.Errorf("token exchange configuration is nil for type tokenExchange")
	}

	// Validate that the referenced Kubernetes secret exists (if ClientSecretRef is provided)
	if tokenExchangeSpec.ClientSecretRef != nil {
		var secret corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{
			Namespace: namespace,
			Name:      tokenExchangeSpec.ClientSecretRef.Name,
		}, &secret); err != nil {
			return fmt.Errorf("failed to get client secret %s/%s: %w",
				namespace, tokenExchangeSpec.ClientSecretRef.Name, err)
		}

		if _, ok := secret.Data[tokenExchangeSpec.ClientSecretRef.Key]; !ok {
			return fmt.Errorf("client secret %s/%s is missing key %q",
				namespace, tokenExchangeSpec.ClientSecretRef.Name, tokenExchangeSpec.ClientSecretRef.Key)
		}
	}

	// Determine header strategy based on ExternalTokenHeaderName
	headerStrategy := "replace" // Default strategy
	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		headerStrategy = "custom"
	}

	// Normalize SubjectTokenType to full URN (accepts both short forms and full URNs)
	normalizedTokenType, err := exchange.NormalizeTokenType(tokenExchangeSpec.SubjectTokenType)
	if err != nil {
		return fmt.Errorf("invalid subject token type: %w", err)
	}

	// Build token exchange configuration
	// Client secret is provided via TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET environment variable
	// to avoid embedding plaintext secrets in the ConfigMap
	tokenExchangeConfig := &tokenexchange.Config{
		TokenURL:                tokenExchangeSpec.TokenURL,
		ClientID:                tokenExchangeSpec.ClientID,
		Audience:                tokenExchangeSpec.Audience,
		Scopes:                  tokenExchangeSpec.Scopes,
		SubjectTokenType:        normalizedTokenType,
		HeaderStrategy:          headerStrategy,
		ExternalTokenHeaderName: tokenExchangeSpec.ExternalTokenHeaderName,
	}

	// Use WithTokenExchangeConfig to add configuration
	// The middleware will be automatically created by PopulateMiddlewareConfigs() in the correct order
	*options = append(*options, runner.WithTokenExchangeConfig(tokenExchangeConfig))

	return nil
}

// addBearerTokenConfig adds bearer token configuration to runner options
func addBearerTokenConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	bearerTokenSpec := externalAuthConfig.Spec.BearerToken
	if bearerTokenSpec == nil {
		return fmt.Errorf("bearer token configuration is nil for type bearerToken")
	}

	if bearerTokenSpec.TokenSecretRef == nil {
		return fmt.Errorf("bearer token configuration is missing TokenSecretRef")
	}

	// Validate secret exists
	var secret corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      bearerTokenSpec.TokenSecretRef.Name,
	}, &secret); err != nil {
		return fmt.Errorf("failed to get bearer token secret %s/%s: %w",
			namespace, bearerTokenSpec.TokenSecretRef.Name, err)
	}

	// Validate key exists
	if _, ok := secret.Data[bearerTokenSpec.TokenSecretRef.Key]; !ok {
		return fmt.Errorf("bearer token secret %s/%s is missing key %q",
			namespace, bearerTokenSpec.TokenSecretRef.Name, bearerTokenSpec.TokenSecretRef.Key)
	}

	// Convert to CLI format: "secret-name,target=bearer_token"
	// Note: The secret name in CLI format must match the Kubernetes Secret name
	// This will be resolved by EnvironmentProvider looking for TOOLHIVE_SECRET_{secret-name}
	cliFormat := fmt.Sprintf("%s,target=bearer_token", bearerTokenSpec.TokenSecretRef.Name)

	// Create remote auth config
	remoteConfig := &remote.Config{
		BearerToken: cliFormat,
	}

	*options = append(*options, runner.WithRemoteAuth(remoteConfig))
	return nil
}

// GenerateBearerTokenEnvVar generates environment variables for bearer token authentication
func GenerateBearerTokenEnvVar(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1beta1.ExternalAuthConfigRef,
	getExternalAuthConfig func(context.Context, client.Client, string, string) (*mcpv1beta1.MCPExternalAuthConfig, error),
) ([]corev1.EnvVar, error) {
	var envVars []corev1.EnvVar

	if externalAuthConfigRef == nil {
		return envVars, nil
	}

	externalAuthConfig, err := getExternalAuthConfig(ctx, c, namespace, externalAuthConfigRef.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig == nil {
		return nil, fmt.Errorf("MCPExternalAuthConfig %s not found", externalAuthConfigRef.Name)
	}

	if externalAuthConfig.Spec.Type != mcpv1beta1.ExternalAuthTypeBearerToken {
		return envVars, nil
	}

	bearerTokenSpec := externalAuthConfig.Spec.BearerToken
	if bearerTokenSpec == nil || bearerTokenSpec.TokenSecretRef == nil {
		return envVars, nil
	}

	// Environment variable name: TOOLHIVE_SECRET_{secret-name}
	envVarName := fmt.Sprintf("TOOLHIVE_SECRET_%s", bearerTokenSpec.TokenSecretRef.Name)

	envVars = append(envVars, corev1.EnvVar{
		Name: envVarName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: bearerTokenSpec.TokenSecretRef.Name,
				},
				Key: bearerTokenSpec.TokenSecretRef.Key,
			},
		},
	})

	return envVars, nil
}

// addAWSStsConfig adds AWS STS configuration to runner options
// This enables OIDC token exchange for AWS credentials using AssumeRoleWithWebIdentity
func addAWSStsConfig(
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	awsStsSpec := externalAuthConfig.Spec.AWSSts
	if awsStsSpec == nil {
		return fmt.Errorf("awsSts configuration is nil for type awsSts")
	}

	// Convert role mappings from CRD to pkg type
	// Priority nil semantics are preserved: nil in CRD → nil in pkg → lowest priority (math.MaxInt)
	var roleMappings []awssts.RoleMapping
	for _, rm := range awsStsSpec.RoleMappings {
		var priority *int
		if rm.Priority != nil {
			p := int(*rm.Priority)
			priority = &p
		}
		roleMappings = append(roleMappings, awssts.RoleMapping{
			RoleArn:  rm.RoleArn,
			Claim:    rm.Claim,
			Matcher:  rm.Matcher,
			Priority: priority,
		})
	}

	// Build AWS STS configuration
	awsStsConfig := &awssts.Config{
		Region:           awsStsSpec.Region,
		Service:          awsStsSpec.Service,
		FallbackRoleArn:  awsStsSpec.FallbackRoleArn,
		RoleMappings:     roleMappings,
		RoleClaim:        awsStsSpec.RoleClaim,
		SessionNameClaim: awsStsSpec.SessionNameClaim,
	}

	// Set session duration if specified
	if awsStsSpec.SessionDuration != nil {
		awsStsConfig.SessionDuration = *awsStsSpec.SessionDuration
	}

	// Use WithAWSStsConfig to add configuration
	// The middleware will be automatically created by PopulateMiddlewareConfigs() in the correct order
	*options = append(*options, runner.WithAWSStsConfig(awsStsConfig))

	return nil
}
