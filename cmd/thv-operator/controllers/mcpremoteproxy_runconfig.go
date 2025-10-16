package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	configMapChecksum "github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/pkg/runner"
	transporttypes "github.com/stacklok/toolhive/pkg/transport/types"
)

// ensureRunConfigConfigMap ensures the RunConfig ConfigMap exists and is up to date for MCPRemoteProxy
func (r *MCPRemoteProxyReconciler) ensureRunConfigConfigMap(ctx context.Context, proxy *mcpv1alpha1.MCPRemoteProxy) error {
	runConfig, err := r.createRunConfigFromMCPRemoteProxy(proxy)
	if err != nil {
		return fmt.Errorf("failed to create RunConfig from MCPRemoteProxy: %w", err)
	}

	// Validate the RunConfig before creating the ConfigMap
	if err := r.validateRunConfigForRemoteProxy(ctx, runConfig); err != nil {
		return fmt.Errorf("invalid RunConfig: %w", err)
	}

	runConfigJSON, err := json.MarshalIndent(runConfig, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal run config: %w", err)
	}

	configMapName := fmt.Sprintf("%s-runconfig", proxy.Name)
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: proxy.Namespace,
			Labels:    labelsForRunConfigRemoteProxy(proxy.Name),
		},
		Data: map[string]string{
			"runconfig.json": string(runConfigJSON),
		},
	}

	// Compute and add content checksum annotation
	checksumCalculator := configMapChecksum.NewRunConfigConfigMapChecksum()
	checksum := checksumCalculator.ComputeConfigMapChecksum(configMap)
	configMap.Annotations = map[string]string{
		"toolhive.stacklok.dev/content-checksum": checksum,
	}

	return r.ensureRunConfigConfigMapResource(ctx, proxy, configMap)
}

// ensureRunConfigConfigMapResource ensures the RunConfig ConfigMap exists and is up to date
func (r *MCPRemoteProxyReconciler) ensureRunConfigConfigMapResource(
	ctx context.Context,
	proxy *mcpv1alpha1.MCPRemoteProxy,
	desired *corev1.ConfigMap,
) error {
	ctxLogger := log.FromContext(ctx)
	current := &corev1.ConfigMap{}
	objectKey := types.NamespacedName{Name: desired.Name, Namespace: desired.Namespace}
	err := r.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(proxy, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for RunConfig ConfigMap: %w", err)
		}

		ctxLogger.Info("RunConfig ConfigMap does not exist, creating", "ConfigMap.Name", desired.Name)
		if err := r.Create(ctx, desired); err != nil {
			return fmt.Errorf("failed to create RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap created", "ConfigMap.Name", desired.Name)
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get RunConfig ConfigMap: %w", err)
	}

	// ConfigMap exists, check if content has changed by comparing checksums
	currentChecksum := current.Annotations["toolhive.stacklok.dev/content-checksum"]
	desiredChecksum := desired.Annotations["toolhive.stacklok.dev/content-checksum"]

	if currentChecksum != desiredChecksum {
		desired.ResourceVersion = current.ResourceVersion
		desired.UID = current.UID

		if err := controllerutil.SetControllerReference(proxy, desired, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference for RunConfig ConfigMap: %w", err)
		}

		ctxLogger.Info("RunConfig ConfigMap content changed, updating",
			"ConfigMap.Name", desired.Name,
			"oldChecksum", currentChecksum,
			"newChecksum", desiredChecksum)
		if err := r.Update(ctx, desired); err != nil {
			return fmt.Errorf("failed to update RunConfig ConfigMap: %w", err)
		}
		ctxLogger.Info("RunConfig ConfigMap updated", "ConfigMap.Name", desired.Name)
	}

	return nil
}

// createRunConfigFromMCPRemoteProxy converts MCPRemoteProxy spec to RunConfig
// Key difference from MCPServer: Sets RemoteURL instead of Image, and Deployer remains nil
func (r *MCPRemoteProxyReconciler) createRunConfigFromMCPRemoteProxy(
	proxy *mcpv1alpha1.MCPRemoteProxy,
) (*runner.RunConfig, error) {
	proxyHost := defaultProxyHost
	if envHost := os.Getenv("TOOLHIVE_PROXY_HOST"); envHost != "" {
		proxyHost = envHost
	}

	port := 8080
	if proxy.Spec.Port != 0 {
		port = int(proxy.Spec.Port)
	}

	// Get tool configuration from MCPToolConfig if referenced
	var toolsFilter []string
	var toolsOverride map[string]runner.ToolOverride

	if proxy.Spec.ToolConfigRef != nil {
		toolConfig, err := GetToolConfigForMCPRemoteProxy(context.Background(), r.Client, proxy)
		if err != nil {
			return nil, fmt.Errorf("failed to get MCPToolConfig: %w", err)
		}

		if toolConfig != nil {
			toolsFilter = toolConfig.Spec.ToolsFilter

			if len(toolConfig.Spec.ToolsOverride) > 0 {
				toolsOverride = make(map[string]runner.ToolOverride)
				for toolName, override := range toolConfig.Spec.ToolsOverride {
					toolsOverride[toolName] = runner.ToolOverride{
						Name:        override.Name,
						Description: override.Description,
					}
				}
			}
		}
	}

	// Determine transport type (default to streamable-http to match CLI)
	transport := proxy.Spec.Transport
	if transport == "" {
		transport = transporttypes.TransportTypeStreamableHTTP.String()
	}

	// Build options for remote proxy
	options := []runner.RunConfigBuilderOption{
		runner.WithName(proxy.Name),
		// Key: Set RemoteURL instead of Image
		runner.WithRemoteURL(proxy.Spec.RemoteURL),
		// Use user-specified transport (sse or streamable-http, both use HTTPTransport internally)
		runner.WithTransportAndPorts(transport, port, 0),
		runner.WithHost(proxyHost),
		runner.WithTrustProxyHeaders(proxy.Spec.TrustProxyHeaders),
		runner.WithToolsFilter(toolsFilter),
	}

	// Add tools override if present
	if toolsOverride != nil {
		options = append(options, runner.WithToolsOverride(toolsOverride))
	}

	// Add telemetry configuration if specified
	addTelemetryConfigOptions(&options, proxy.Spec.Telemetry, proxy.Name)

	// Add authorization configuration if specified
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()

	if err := r.addAuthzConfigOptionsForRemoteProxy(ctx, proxy, &options); err != nil {
		return nil, fmt.Errorf("failed to process AuthzConfig: %w", err)
	}

	// Add OIDC configuration (required for proxy mode)
	if err := r.addOIDCConfigOptionsForRemoteProxy(ctx, &options, proxy); err != nil {
		return nil, fmt.Errorf("failed to process OIDCConfig: %w", err)
	}

	// Add external auth configuration if specified
	if err := r.addExternalAuthConfigOptionsForRemoteProxy(ctx, proxy, &options); err != nil {
		return nil, fmt.Errorf("failed to process ExternalAuthConfig: %w", err)
	}

	// Add audit configuration if specified
	addAuditConfigOptions(&options, proxy.Spec.Audit)

	// Use the RunConfigBuilder for operator context
	// Deployer is nil for remote proxies because they connect to external services
	// and do not require container deployment (unlike MCPServer which deploys containers)
	return runner.NewOperatorRunConfigBuilder(
		context.Background(),
		nil,
		nil,
		nil,
		options...,
	)
}

// addAuthzConfigOptionsForRemoteProxy adds authorization configuration options for remote proxy
func (r *MCPRemoteProxyReconciler) addAuthzConfigOptionsForRemoteProxy(
	ctx context.Context,
	proxy *mcpv1alpha1.MCPRemoteProxy,
	options *[]runner.RunConfigBuilderOption,
) error {
	return AddAuthzConfigOptions(ctx, r.Client, proxy.Namespace, proxy.Spec.AuthzConfig, options)
}

// addOIDCConfigOptionsForRemoteProxy adds OIDC authentication configuration options for remote proxy
func (r *MCPRemoteProxyReconciler) addOIDCConfigOptionsForRemoteProxy(
	ctx context.Context,
	options *[]runner.RunConfigBuilderOption,
	proxy *mcpv1alpha1.MCPRemoteProxy,
) error {
	resolver := oidc.NewResolver(r.Client)
	oidcConfig, err := resolver.Resolve(ctx, proxy)
	if err != nil {
		return fmt.Errorf("failed to resolve OIDC configuration: %w", err)
	}

	if oidcConfig == nil {
		return nil
	}

	*options = append(*options, runner.WithOIDCConfig(
		oidcConfig.Issuer,
		oidcConfig.Audience,
		oidcConfig.JWKSURL,
		oidcConfig.IntrospectionURL,
		oidcConfig.ClientID,
		oidcConfig.ClientSecret,
		oidcConfig.ThvCABundlePath,
		oidcConfig.JWKSAuthTokenPath,
		oidcConfig.ResourceURL,
		oidcConfig.JWKSAllowPrivateIP,
	))

	return nil
}

// addExternalAuthConfigOptionsForRemoteProxy adds external authentication configuration for remote proxy
func (r *MCPRemoteProxyReconciler) addExternalAuthConfigOptionsForRemoteProxy(
	ctx context.Context,
	proxy *mcpv1alpha1.MCPRemoteProxy,
	options *[]runner.RunConfigBuilderOption,
) error {
	if proxy.Spec.ExternalAuthConfigRef == nil {
		return nil
	}

	externalAuthConfig, err := GetExternalAuthConfigForMCPRemoteProxy(ctx, r.Client, proxy)
	if err != nil {
		return fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	if externalAuthConfig.Spec.Type != mcpv1alpha1.ExternalAuthTypeTokenExchange {
		return fmt.Errorf("unsupported external auth type: %s", externalAuthConfig.Spec.Type)
	}

	tokenExchangeSpec := externalAuthConfig.Spec.TokenExchange
	if tokenExchangeSpec == nil {
		return fmt.Errorf("token exchange configuration is nil for type tokenExchange")
	}

	// Validate that the referenced Kubernetes secret exists
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: proxy.Namespace,
		Name:      tokenExchangeSpec.ClientSecretRef.Name,
	}, &secret); err != nil {
		return fmt.Errorf("failed to get client secret %s/%s: %w",
			proxy.Namespace, tokenExchangeSpec.ClientSecretRef.Name, err)
	}

	if _, ok := secret.Data[tokenExchangeSpec.ClientSecretRef.Key]; !ok {
		return fmt.Errorf("client secret %s/%s is missing key %q",
			proxy.Namespace, tokenExchangeSpec.ClientSecretRef.Name, tokenExchangeSpec.ClientSecretRef.Key)
	}

	scopes := tokenExchangeSpec.Scopes

	headerStrategy := "replace"
	if tokenExchangeSpec.ExternalTokenHeaderName != "" {
		headerStrategy = "custom"
	}

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

	middlewareParams := map[string]interface{}{
		"token_exchange_config": tokenExchangeConfig,
	}

	paramsJSON, err := json.Marshal(middlewareParams)
	if err != nil {
		return fmt.Errorf("failed to marshal token exchange middleware parameters: %w", err)
	}

	middlewareConfig := transporttypes.MiddlewareConfig{
		Type:       "tokenexchange",
		Parameters: json.RawMessage(paramsJSON),
	}

	*options = append(*options, runner.WithMiddlewareConfig([]transporttypes.MiddlewareConfig{middlewareConfig}))

	return nil
}

// validateRunConfigForRemoteProxy validates a RunConfig for remote proxy deployments
func (*MCPRemoteProxyReconciler) validateRunConfigForRemoteProxy(ctx context.Context, config *runner.RunConfig) error {
	if config == nil {
		return fmt.Errorf("RunConfig cannot be nil")
	}

	if config.RemoteURL == "" {
		return fmt.Errorf("remoteURL is required for remote proxy")
	}

	if config.Name == "" {
		return fmt.Errorf("name is required")
	}

	// SSE or StreamableHTTP transport is used for remote proxies (both use HTTPTransport internally)
	if config.Transport != transporttypes.TransportTypeSSE && config.Transport != transporttypes.TransportTypeStreamableHTTP {
		return fmt.Errorf("transport must be SSE or StreamableHTTP for remote proxy, got: %s", config.Transport)
	}

	if config.Port <= 0 {
		return fmt.Errorf("port is required for remote proxy")
	}

	if config.Host == "" {
		return fmt.Errorf("host is required for remote proxy")
	}

	// Validate tools filter
	for _, tool := range config.ToolsFilter {
		if tool == "" {
			return fmt.Errorf("tool filter cannot contain empty values")
		}
	}

	ctxLogger := log.FromContext(ctx)
	ctxLogger.V(1).Info("RunConfig validation passed for remote proxy", "name", config.Name)
	return nil
}

// labelsForRunConfigRemoteProxy returns labels for run config ConfigMap for remote proxy
func labelsForRunConfigRemoteProxy(proxyName string) map[string]string {
	return map[string]string{
		"toolhive.stacklok.io/component":        "run-config",
		"toolhive.stacklok.io/mcp-remote-proxy": proxyName,
		"toolhive.stacklok.io/managed-by":       "toolhive-operator",
	}
}
