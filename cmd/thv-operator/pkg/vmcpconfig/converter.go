// Package vmcpconfig provides conversion logic from VirtualMCPServer CRD to vmcp Config
package vmcpconfig

import (
	"context"
	"encoding/json"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	// authzLabelValueInline is the string value for inline authz configuration
	authzLabelValueInline = "inline"
	// conflictResolutionPrefix is the string value for prefix conflict resolution strategy
	conflictResolutionPrefix = "prefix"
)

// Converter converts VirtualMCPServer CRD specs to vmcp Config
type Converter struct{}

// NewConverter creates a new Converter instance
func NewConverter() *Converter {
	return &Converter{}
}

// Convert converts VirtualMCPServer CRD spec to vmcp Config
//
//nolint:unparam // error return reserved for future reference resolution
func (c *Converter) Convert(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*vmcpconfig.Config, error) {
	config := &vmcpconfig.Config{
		Name:  vmcp.Name,
		Group: vmcp.Spec.GroupRef.Name,
	}

	// Convert IncomingAuth
	if vmcp.Spec.IncomingAuth != nil {
		config.IncomingAuth = c.convertIncomingAuth(ctx, vmcp)
	}

	// Convert OutgoingAuth - always set with defaults if not specified
	if vmcp.Spec.OutgoingAuth != nil {
		config.OutgoingAuth = c.convertOutgoingAuth(ctx, vmcp)
	} else {
		// Provide default outgoing auth config
		config.OutgoingAuth = &vmcpconfig.OutgoingAuthConfig{
			Source: "discovered", // Default to discovered mode
		}
	}

	// Convert Aggregation - always set with defaults if not specified
	if vmcp.Spec.Aggregation != nil {
		config.Aggregation = c.convertAggregation(ctx, vmcp)
	} else {
		// Provide default aggregation config with prefix conflict resolution
		config.Aggregation = &vmcpconfig.AggregationConfig{
			ConflictResolution: conflictResolutionPrefix, // Default to prefix strategy
			ConflictResolutionConfig: &vmcpconfig.ConflictResolutionConfig{
				PrefixFormat: "{workload}_", // Default prefix format
			},
		}
	}

	// Convert CompositeTools
	if len(vmcp.Spec.CompositeTools) > 0 {
		config.CompositeTools = c.convertCompositeTools(ctx, vmcp)
	}

	// Convert Operational
	if vmcp.Spec.Operational != nil {
		config.Operational = c.convertOperational(ctx, vmcp)
	}

	// Apply operational defaults (fills missing values)
	config.EnsureOperationalDefaults()

	return config, nil
}

// convertIncomingAuth converts IncomingAuthConfig from CRD to vmcp config
func (*Converter) convertIncomingAuth(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.IncomingAuthConfig {
	incoming := &vmcpconfig.IncomingAuthConfig{
		Type: vmcp.Spec.IncomingAuth.Type,
	}

	// Convert OIDC configuration if present
	if vmcp.Spec.IncomingAuth.OIDCConfig != nil {
		// Handle inline OIDC configuration
		if vmcp.Spec.IncomingAuth.OIDCConfig.Type == authzLabelValueInline && vmcp.Spec.IncomingAuth.OIDCConfig.Inline != nil {
			inline := vmcp.Spec.IncomingAuth.OIDCConfig.Inline
			oidcConfig := &vmcpconfig.OIDCConfig{
				Issuer:                          inline.Issuer,
				ClientID:                        inline.ClientID, // Note: API uses clientId (camelCase) but config uses ClientID
				Audience:                        inline.Audience,
				Resource:                        vmcp.Spec.IncomingAuth.OIDCConfig.ResourceURL,
				Scopes:                          nil, // TODO: Add scopes if needed
				ProtectedResourceAllowPrivateIP: inline.ProtectedResourceAllowPrivateIP,
				InsecureAllowHTTP:               inline.InsecureAllowHTTP,
			}

			// Handle client secret - always use environment variable reference for security
			// Both ClientSecretRef (reference to existing secret) and ClientSecret (literal value)
			// are mounted as environment variables by the deployment controller
			if inline.ClientSecretRef != nil || inline.ClientSecret != "" {
				// Generate environment variable name that will be mounted in the deployment
				// The deployment controller will mount the secret (either from ClientSecretRef or
				// from a generated secret for ClientSecret literal values)
				oidcConfig.ClientSecretEnv = "VMCP_OIDC_CLIENT_SECRET"
			}

			incoming.OIDC = oidcConfig
		} else {
			// TODO: Handle configMap and kubernetes types
			// For now, create empty config to avoid nil pointer
			incoming.OIDC = &vmcpconfig.OIDCConfig{}
		}
	}

	// Convert authorization configuration
	if vmcp.Spec.IncomingAuth.AuthzConfig != nil {
		// Map Kubernetes API types to vmcp config types
		// API "inline" maps to vmcp "cedar"
		authzType := vmcp.Spec.IncomingAuth.AuthzConfig.Type
		if authzType == authzLabelValueInline {
			authzType = "cedar"
		}

		incoming.Authz = &vmcpconfig.AuthzConfig{
			Type: authzType,
		}

		// Handle inline policies
		if vmcp.Spec.IncomingAuth.AuthzConfig.Type == authzLabelValueInline && vmcp.Spec.IncomingAuth.AuthzConfig.Inline != nil {
			incoming.Authz.Policies = vmcp.Spec.IncomingAuth.AuthzConfig.Inline.Policies
		}
		// TODO: Load policies from ConfigMap if Type is "configMap"
	}

	return incoming
}

// convertOutgoingAuth converts OutgoingAuthConfig from CRD to vmcp config
func (c *Converter) convertOutgoingAuth(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.OutgoingAuthConfig {
	outgoing := &vmcpconfig.OutgoingAuthConfig{
		Source:   vmcp.Spec.OutgoingAuth.Source,
		Backends: make(map[string]*authtypes.BackendAuthStrategy),
	}

	// Convert Default
	if vmcp.Spec.OutgoingAuth.Default != nil {
		outgoing.Default = c.convertBackendAuthConfig(vmcp.Spec.OutgoingAuth.Default)
	}

	// Convert per-backend overrides
	for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		outgoing.Backends[backendName] = c.convertBackendAuthConfig(&backendAuth)
	}

	return outgoing
}

// convertBackendAuthConfig converts BackendAuthConfig from CRD to vmcp config
func (*Converter) convertBackendAuthConfig(
	crdConfig *mcpv1alpha1.BackendAuthConfig,
) *authtypes.BackendAuthStrategy {
	strategy := &authtypes.BackendAuthStrategy{
		Type: crdConfig.Type,
	}

	// Note: When Type is "external_auth_config_ref", the actual MCPExternalAuthConfig
	// resource should be resolved at runtime and its configuration (TokenExchange or
	// HeaderInjection) should be populated into the corresponding typed fields.
	// This conversion happens during server initialization when the referenced
	// MCPExternalAuthConfig can be looked up.

	return strategy
}

// convertAggregation converts AggregationConfig from CRD to vmcp config
func (*Converter) convertAggregation(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.AggregationConfig {
	agg := &vmcpconfig.AggregationConfig{}

	// Convert conflict resolution strategy
	switch vmcp.Spec.Aggregation.ConflictResolution {
	case mcpv1alpha1.ConflictResolutionPrefix:
		agg.ConflictResolution = conflictResolutionPrefix
	case mcpv1alpha1.ConflictResolutionPriority:
		agg.ConflictResolution = "priority"
	case mcpv1alpha1.ConflictResolutionManual:
		agg.ConflictResolution = "manual"
	default:
		agg.ConflictResolution = conflictResolutionPrefix // default
	}

	// Convert conflict resolution config
	if vmcp.Spec.Aggregation.ConflictResolutionConfig != nil {
		agg.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat:  vmcp.Spec.Aggregation.ConflictResolutionConfig.PrefixFormat,
			PriorityOrder: vmcp.Spec.Aggregation.ConflictResolutionConfig.PriorityOrder,
		}
	} else if agg.ConflictResolution == conflictResolutionPrefix {
		// Provide default prefix format if using prefix strategy without explicit config
		agg.ConflictResolutionConfig = &vmcpconfig.ConflictResolutionConfig{
			PrefixFormat: "{workload}_",
		}
	}

	// Convert per-workload tool configs
	if len(vmcp.Spec.Aggregation.Tools) > 0 {
		agg.Tools = make([]*vmcpconfig.WorkloadToolConfig, 0, len(vmcp.Spec.Aggregation.Tools))
		for _, toolConfig := range vmcp.Spec.Aggregation.Tools {
			wtc := &vmcpconfig.WorkloadToolConfig{
				Workload: toolConfig.Workload,
				Filter:   toolConfig.Filter,
			}

			// Convert overrides
			if len(toolConfig.Overrides) > 0 {
				wtc.Overrides = make(map[string]*vmcpconfig.ToolOverride)
				for toolName, override := range toolConfig.Overrides {
					wtc.Overrides[toolName] = &vmcpconfig.ToolOverride{
						Name:        override.Name,
						Description: override.Description,
					}
				}
			}

			agg.Tools = append(agg.Tools, wtc)
		}
	}

	return agg
}

// convertCompositeTools converts CompositeToolSpec from CRD to vmcp config
func (*Converter) convertCompositeTools(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) []*vmcpconfig.CompositeToolConfig {
	compositeTools := make([]*vmcpconfig.CompositeToolConfig, 0, len(vmcp.Spec.CompositeTools))

	for _, crdTool := range vmcp.Spec.CompositeTools {
		tool := &vmcpconfig.CompositeToolConfig{
			Name:        crdTool.Name,
			Description: crdTool.Description,
			Steps:       make([]*vmcpconfig.WorkflowStepConfig, 0, len(crdTool.Steps)),
		}

		// Parse timeout
		if crdTool.Timeout != "" {
			if duration, err := time.ParseDuration(crdTool.Timeout); err == nil {
				tool.Timeout = vmcpconfig.Duration(duration)
			}
		}

		// Convert parameters from runtime.RawExtension to map[string]any
		if crdTool.Parameters != nil && len(crdTool.Parameters.Raw) > 0 {
			var params map[string]any
			if err := json.Unmarshal(crdTool.Parameters.Raw, &params); err != nil {
				// Log warning but continue - validation should have caught this at admission time
				ctxLogger := log.FromContext(ctx)
				ctxLogger.Error(err, "failed to unmarshal composite tool parameters",
					"tool", crdTool.Name, "raw", string(crdTool.Parameters.Raw))
			} else {
				tool.Parameters = params
			}
		}

		// Convert steps
		for _, crdStep := range crdTool.Steps {
			step := &vmcpconfig.WorkflowStepConfig{
				ID:        crdStep.ID,
				Type:      crdStep.Type,
				Tool:      crdStep.Tool,
				Arguments: convertArguments(crdStep.Arguments),
				Message:   crdStep.Message,
				Condition: crdStep.Condition,
				DependsOn: crdStep.DependsOn,
			}

			// Parse timeout
			if crdStep.Timeout != "" {
				if duration, err := time.ParseDuration(crdStep.Timeout); err == nil {
					step.Timeout = vmcpconfig.Duration(duration)
				}
			}

			// Convert error handling
			if crdStep.OnError != nil {
				stepError := &vmcpconfig.StepErrorHandling{
					Action:     crdStep.OnError.Action,
					RetryCount: crdStep.OnError.MaxRetries,
				}
				if crdStep.OnError.RetryDelay != "" {
					if duration, err := time.ParseDuration(crdStep.OnError.RetryDelay); err != nil {
						// Log warning but continue - validation should have caught this at admission time
						ctxLogger := log.FromContext(ctx)
						ctxLogger.Error(err, "failed to parse retry delay",
							"step", crdStep.ID, "retryDelay", crdStep.OnError.RetryDelay)
					} else {
						stepError.RetryDelay = vmcpconfig.Duration(duration)
					}
				}
				step.OnError = stepError
			}

			tool.Steps = append(tool.Steps, step)
		}

		// Convert output configuration
		if crdTool.Output != nil {
			tool.Output = convertOutputSpec(ctx, crdTool.Output)
		}

		compositeTools = append(compositeTools, tool)
	}

	return compositeTools
}

// convertArguments converts string arguments to any type for template expansion
func convertArguments(args map[string]string) map[string]any {
	result := make(map[string]any, len(args))
	for k, v := range args {
		result[k] = v
	}
	return result
}

// convertOutputSpec converts OutputSpec from CRD to vmcp config OutputConfig
func convertOutputSpec(ctx context.Context, crdOutput *mcpv1alpha1.OutputSpec) *vmcpconfig.OutputConfig {
	if crdOutput == nil {
		return nil
	}

	output := &vmcpconfig.OutputConfig{
		Properties: make(map[string]vmcpconfig.OutputProperty, len(crdOutput.Properties)),
		Required:   crdOutput.Required,
	}

	// Convert properties
	for propName, propSpec := range crdOutput.Properties {
		output.Properties[propName] = convertOutputProperty(ctx, propName, propSpec)
	}

	return output
}

// convertOutputProperty converts OutputPropertySpec from CRD to vmcp config OutputProperty
func convertOutputProperty(
	ctx context.Context, propName string, crdProp mcpv1alpha1.OutputPropertySpec,
) vmcpconfig.OutputProperty {
	prop := vmcpconfig.OutputProperty{
		Type:        crdProp.Type,
		Description: crdProp.Description,
		Value:       crdProp.Value,
	}

	// Convert nested properties for object types
	if len(crdProp.Properties) > 0 {
		prop.Properties = make(map[string]vmcpconfig.OutputProperty, len(crdProp.Properties))
		for nestedName, nestedSpec := range crdProp.Properties {
			prop.Properties[nestedName] = convertOutputProperty(ctx, propName+"."+nestedName, nestedSpec)
		}
	}

	// Convert default value from runtime.RawExtension to any
	// RawExtension.Raw contains JSON bytes. json.Unmarshal correctly handles:
	// - JSON strings: "hello" -> Go string "hello"
	// - JSON numbers: 42 -> Go float64(42)
	// - JSON booleans: true -> Go bool true
	// - JSON objects: {"key":"value"} -> Go map[string]any
	// - JSON arrays: [1,2,3] -> Go []any
	if crdProp.Default != nil && len(crdProp.Default.Raw) > 0 {
		var defaultVal any
		if err := json.Unmarshal(crdProp.Default.Raw, &defaultVal); err != nil {
			// Log warning but continue - invalid defaults will be caught at runtime
			ctxLogger := log.FromContext(ctx)
			ctxLogger.Error(err, "failed to unmarshal output property default value",
				"property", propName, "raw", string(crdProp.Default.Raw))
		} else {
			prop.Default = defaultVal
		}
	}

	return prop
}

// convertOperational converts OperationalConfig from CRD to vmcp config
func (*Converter) convertOperational(
	_ context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) *vmcpconfig.OperationalConfig {
	operational := &vmcpconfig.OperationalConfig{}

	if vmcp.Spec.Operational.Timeouts != nil {
		operational.Timeouts = &vmcpconfig.TimeoutConfig{
			PerWorkload: make(map[string]vmcpconfig.Duration),
		}

		// Parse default timeout
		if vmcp.Spec.Operational.Timeouts.Default != "" {
			if duration, err := time.ParseDuration(vmcp.Spec.Operational.Timeouts.Default); err == nil {
				operational.Timeouts.Default = vmcpconfig.Duration(duration)
			}
		}

		// Parse per-workload timeouts
		for workload, timeoutStr := range vmcp.Spec.Operational.Timeouts.PerWorkload {
			if duration, err := time.ParseDuration(timeoutStr); err == nil {
				operational.Timeouts.PerWorkload[workload] = vmcpconfig.Duration(duration)
			}
		}
	}

	if vmcp.Spec.Operational.FailureHandling != nil {
		operational.FailureHandling = &vmcpconfig.FailureHandlingConfig{
			UnhealthyThreshold: vmcp.Spec.Operational.FailureHandling.UnhealthyThreshold,
			PartialFailureMode: vmcp.Spec.Operational.FailureHandling.PartialFailureMode,
		}

		// Parse health check interval
		if vmcp.Spec.Operational.FailureHandling.HealthCheckInterval != "" {
			if duration, err := time.ParseDuration(vmcp.Spec.Operational.FailureHandling.HealthCheckInterval); err == nil {
				operational.FailureHandling.HealthCheckInterval = vmcpconfig.Duration(duration)
			}
		}

		// Convert circuit breaker config
		if vmcp.Spec.Operational.FailureHandling.CircuitBreaker != nil {
			operational.FailureHandling.CircuitBreaker = &vmcpconfig.CircuitBreakerConfig{
				Enabled:          vmcp.Spec.Operational.FailureHandling.CircuitBreaker.Enabled,
				FailureThreshold: vmcp.Spec.Operational.FailureHandling.CircuitBreaker.FailureThreshold,
			}

			// Parse circuit breaker timeout
			if vmcp.Spec.Operational.FailureHandling.CircuitBreaker.Timeout != "" {
				if duration, err := time.ParseDuration(vmcp.Spec.Operational.FailureHandling.CircuitBreaker.Timeout); err == nil {
					operational.FailureHandling.CircuitBreaker.Timeout = vmcpconfig.Duration(duration)
				}
			}
		}
	}

	return operational
}
