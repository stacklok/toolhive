package controllerutil

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/auth/tokenexchange"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
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

	return envVars, nil
}

// AddExternalAuthConfigOptions adds external authentication configuration options to builder options
// This creates middleware configuration for token exchange and is shared between MCPServer and MCPRemoteProxy
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

	// Only token exchange type is supported currently
	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeTokenExchange {
		return fmt.Errorf("unsupported external auth type: %s", externalAuthConfig.Spec.Type)
	}

	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return fmt.Errorf("token exchange configuration is nil for type tokenExchange")
	}

	// Validate that the referenced Kubernetes secret exists
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

	// Use scopes array directly from spec
	scopes := tokenExchangeSpec.Scopes

	// Determine header strategy based on ExternalTokenHeaderName
	headerStrategy := "replace" // Default strategy
	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		headerStrategy = "custom"
	}

	// Build token exchange middleware configuration
	// Client secret is provided via TOOLHIVE_TOKEN_EXCHANGE_CLIENT_SECRET environment variable
	// to avoid embedding plaintext secrets in the ConfigMap
	tokenExchangeConfig := map[string]interface{}{
		"token_url": tokenExchangeSpec.TokenURL,
		"client_id": tokenExchangeSpec.ClientID,
		"audience":  tokenExchangeSpec.Audience,
	}

	if len(scopes) > 0 {
		tokenExchangeConfig["scopes"] = scopes
	}

	if headerStrategy != "" {
		tokenExchangeConfig["header_strategy"] = headerStrategy
	}

	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		tokenExchangeConfig["external_token_header_name"] = tokenExchangeSpec.ExternalTokenHeaderName
	}

	// Create middleware parameters
	middlewareParams := map[string]interface{}{
		"token_exchange_config": tokenExchangeConfig,
	}

	// Marshal parameters to JSON
	paramsJSON, err := json.Marshal(middlewareParams)
	if err != nil {
		return fmt.Errorf("failed to marshal token exchange middleware parameters: %w", err)
	}

	// Create middleware config
	middlewareConfig := transporttypes.MiddlewareConfig{
		Type:       tokenexchange.MiddlewareType,
		Parameters: json.RawMessage(paramsJSON),
	}

	// Add to options using the WithMiddlewareConfig builder option
	*options = append(*options, runner.WithMiddlewareConfig([]transporttypes.MiddlewareConfig{middlewareConfig}))

	return nil
}
