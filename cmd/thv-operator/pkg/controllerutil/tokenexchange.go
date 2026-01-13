package controllerutil

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/auth/awssts"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/runner"
)

// GenerateOpenTelemetryEnvVars generates OpenTelemetry environment variables
func GenerateOpenTelemetryEnvVars(
	telemetryConfig *mcpv1alpha1.TelemetryConfig,
	resourceName string,
	namespace string,
) []corev1.EnvVar {
	var envVars []corev1.EnvVar

	if telemetryConfig == nil || telemetryConfig.OpenTelemetry == nil {
		return envVars
	}

	otel := telemetryConfig.OpenTelemetry

	serviceName := otel.ServiceName
	if serviceName == "" {
		serviceName = resourceName
	}

	envVars = append(envVars, corev1.EnvVar{
		Name:  "OTEL_RESOURCE_ATTRIBUTES",
		Value: fmt.Sprintf("service.name=%s,service.namespace=%s", serviceName, namespace),
	})

	return envVars
}

// GenerateTokenExchangeEnvVars generates environment variables for token exchange
func GenerateTokenExchangeEnvVars(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
	getExternalAuthConfig func(context.Context, client.Client, string, string) (*mcpv1alpha1.MCPExternalAuthConfig, error),
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

	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeTokenExchange {
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
func AddExternalAuthConfigOptions(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfigRef *mcpv1alpha1.ExternalAuthConfigRef,
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

	// Handle different auth types
	switch externalAuthConfig.Spec.Type {
	case mcpv1alpha1.ExternalAuthTypeTokenExchange:
		return addTokenExchangeConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1alpha1.ExternalAuthTypeHeaderInjection:
		return addHeaderInjectionConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1alpha1.ExternalAuthTypeAWSSts:
		return addAWSStsConfig(ctx, c, namespace, externalAuthConfig, options)
	case mcpv1alpha1.ExternalAuthTypeUnauthenticated:
		// No config to add for unauthenticated
		return nil
	default:
		return fmt.Errorf("unsupported external auth type: %s", externalAuthConfig.Spec.Type)
	}
}

func addTokenExchangeConfig(
	ctx context.Context,
	c client.Client,
	namespace string,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
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
	normalizedTokenType, err := tokenexchange.NormalizeTokenType(tokenExchangeSpec.SubjectTokenType)
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

// addHeaderInjectionConfig adds header injection configuration to runner options
// For now, this is a no-op as header injection for MCPServer is not implemented
// Header injection is primarily used for vMCP outgoing auth, not for MCPServer incoming auth
func addHeaderInjectionConfig(
	_ context.Context,
	_ client.Client,
	_ string,
	_ *mcpv1alpha1.MCPExternalAuthConfig,
	_ *[]runner.RunConfigBuilderOption,
) error {
	// Header injection for MCPServer is not yet implemented
	// This is a placeholder to avoid the "unsupported auth type" error
	// MCPServer's ExternalAuthConfigRef is meant for incoming auth configuration
	// but header injection doesn't make sense in that context
	return nil
}

// addAWSStsConfig adds AWS STS configuration to runner options
func addAWSStsConfig(
	_ context.Context,
	_ client.Client,
	_ string,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
	options *[]runner.RunConfigBuilderOption,
) error {
	awsStsSpec := externalAuthConfig.Spec.AWSSts
	if awsStsSpec == nil {
		return fmt.Errorf("AWS STS configuration is nil for type awsSts")
	}

	// Convert CRD RoleMappings to runtime RoleMappings
	var roleMappings []awssts.RoleMapping
	for _, mapping := range awsStsSpec.RoleMappings {
		priority := 0
		if mapping.Priority != nil {
			priority = int(*mapping.Priority)
		}

		roleMappings = append(roleMappings, awssts.RoleMapping{
			Claim:    mapping.Claim,
			RoleArn:  mapping.RoleArn,
			Priority: priority,
		})
	}

	// Build AWS STS configuration
	awsStsConfig := &awssts.Config{
		Region:       awsStsSpec.Region,
		Service:      awsStsSpec.Service,
		RoleArn:      awsStsSpec.RoleArn,
		RoleMappings: roleMappings,
		RoleClaim:    awsStsSpec.RoleClaim,
	}

	// Set SessionDuration if specified
	if awsStsSpec.SessionDuration != nil {
		awsStsConfig.SessionDuration = *awsStsSpec.SessionDuration
	}

	// Use WithAWSStsConfig to add configuration
	// The middleware will be automatically created by PopulateMiddlewareConfigs() in the correct order
	*options = append(*options, runner.WithAWSStsConfig(awsStsConfig))

	return nil
}
