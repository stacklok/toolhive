package v1

import (
	"context"
	"fmt"

	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/validation"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// WorkloadService handles business logic for workload operations
type WorkloadService struct {
	workloadManager  workloads.Manager
	groupManager     groups.Manager
	secretsProvider  secrets.Provider
	containerRuntime runtime.Runtime
	debugMode        bool
	imageRetriever   retriever.Retriever
}

// NewWorkloadService creates a new WorkloadService instance
func NewWorkloadService(
	workloadManager workloads.Manager,
	groupManager groups.Manager,
	secretsProvider secrets.Provider,
	containerRuntime runtime.Runtime,
	debugMode bool,
) *WorkloadService {
	return &WorkloadService{
		workloadManager:  workloadManager,
		groupManager:     groupManager,
		secretsProvider:  secretsProvider,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
		imageRetriever:   retriever.GetMCPServer,
	}
}

// CreateWorkloadFromRequest creates a workload from a request
func (s *WorkloadService) CreateWorkloadFromRequest(ctx context.Context, req *createRequest) (*runner.RunConfig, error) {
	// Build the full run config
	runConfig, err := s.BuildFullRunConfig(ctx, req)
	if err != nil {
		return nil, err
	}

	// Save the workload state
	if err := runConfig.SaveState(ctx); err != nil {
		logger.Errorf("Failed to save workload config: %v", err)
		return nil, fmt.Errorf("failed to save workload config: %w", err)
	}

	// Start workload
	if err := s.workloadManager.RunWorkloadDetached(ctx, runConfig); err != nil {
		logger.Errorf("Failed to start workload: %v", err)
		return nil, fmt.Errorf("failed to start workload: %w", err)
	}

	return runConfig, nil
}

// UpdateWorkloadFromRequest updates a workload from a request
func (s *WorkloadService) UpdateWorkloadFromRequest(ctx context.Context, name string, req *createRequest) (*runner.RunConfig, error) { //nolint:lll
	// Build the full run config
	runConfig, err := s.BuildFullRunConfig(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to build workload config: %w", err)
	}

	// Use the manager's UpdateWorkload method to handle the lifecycle
	if _, err := s.workloadManager.UpdateWorkload(ctx, name, runConfig); err != nil {
		return nil, fmt.Errorf("failed to update workload: %w", err)
	}

	return runConfig, nil
}

// BuildFullRunConfig builds a complete RunConfig
func (s *WorkloadService) BuildFullRunConfig(ctx context.Context, req *createRequest) (*runner.RunConfig, error) {
	// Default proxy mode to SSE if not specified
	if !types.IsValidProxyMode(req.ProxyMode) {
		if req.ProxyMode == "" {
			req.ProxyMode = types.ProxyModeSSE.String()
		} else {
			return nil, fmt.Errorf("%w: %s", retriever.ErrInvalidRunConfig, fmt.Sprintf("Invalid proxy_mode: %s", req.ProxyMode))
		}
	}

	// Default group if not specified
	groupName := req.Group
	if groupName == "" {
		groupName = groups.DefaultGroup
	}

	// Validate that the group exists
	exists, err := s.groupManager.Exists(ctx, groupName)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("group '%s' does not exist", groupName)
	}

	var remoteAuthConfig *runner.RemoteAuthConfig
	var imageURL string
	var imageMetadata *registry.ImageMetadata

	if req.URL != "" {
		// Configure remote authentication if OAuth config is provided
		if req.Transport == "" {
			req.Transport = types.TransportTypeStreamableHTTP.String()
		}
		remoteAuthConfig, err = s.createRequestToRemoteAuthConfig(ctx, req)
		if err != nil {
			return nil, err
		}
	} else {
		var serverMetadata registry.ServerMetadata
		// Fetch or build the requested image
		imageURL, serverMetadata, err = s.imageRetriever(
			ctx,
			req.Image,
			"", // We do not let the user specify a CA cert path here.
			retriever.VerifyImageWarn,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve MCP server image: %w", err)
		}

		if remoteServerMetadata, ok := serverMetadata.(*registry.RemoteServerMetadata); ok {
			if remoteServerMetadata.OAuthConfig != nil {
				clientSecret, err := s.resolveClientSecret(ctx, req.OAuthConfig.ClientSecret)
				if err != nil {
					return nil, err
				}
				remoteAuthConfig = &runner.RemoteAuthConfig{
					ClientID:     req.OAuthConfig.ClientID,
					ClientSecret: clientSecret,
					Scopes:       remoteServerMetadata.OAuthConfig.Scopes,
					CallbackPort: remoteServerMetadata.OAuthConfig.CallbackPort,
					Issuer:       remoteServerMetadata.OAuthConfig.Issuer,
					AuthorizeURL: remoteServerMetadata.OAuthConfig.AuthorizeURL,
					TokenURL:     remoteServerMetadata.OAuthConfig.TokenURL,
					OAuthParams:  remoteServerMetadata.OAuthConfig.OAuthParams,
					Headers:      remoteServerMetadata.Headers,
					EnvVars:      remoteServerMetadata.EnvVars,
				}
			}
		}
		// Handle server metadata - API only supports container servers
		imageMetadata, _ = serverMetadata.(*registry.ImageMetadata)
	}

	// Build RunConfig
	runSecrets := secrets.SecretParametersToCLI(req.Secrets)

	toolsOverride := make(map[string]runner.ToolOverride)
	for toolName, toolOverride := range req.ToolsOverride {
		toolsOverride[toolName] = runner.ToolOverride{
			Name:        toolOverride.Name,
			Description: toolOverride.Description,
		}
	}

	options := []runner.RunConfigBuilderOption{
		runner.WithRuntime(s.containerRuntime),
		runner.WithCmdArgs(req.CmdArguments),
		runner.WithName(req.Name),
		runner.WithGroup(groupName),
		runner.WithImage(imageURL),
		runner.WithRemoteURL(req.URL),
		runner.WithRemoteAuth(remoteAuthConfig),
		runner.WithHost(req.Host),
		runner.WithTargetHost(transport.LocalhostIPv4),
		runner.WithDebug(s.debugMode),
		runner.WithVolumes(req.Volumes),
		runner.WithSecrets(runSecrets),
		runner.WithAuthzConfigPath(req.AuthzConfig),
		runner.WithAuditConfigPath(""),
		runner.WithPermissionProfile(req.PermissionProfile),
		runner.WithNetworkIsolation(req.NetworkIsolation),
		runner.WithK8sPodPatch(""),
		runner.WithProxyMode(types.ProxyMode(req.ProxyMode)),
		runner.WithTransportAndPorts(req.Transport, 0, req.TargetPort),
		runner.WithAuditEnabled(false, ""),
		runner.WithOIDCConfig(req.OIDC.Issuer, req.OIDC.Audience, req.OIDC.JwksURL, req.OIDC.ClientID,
			"", "", "", "", "", false),
		runner.WithToolsFilter(req.ToolsFilter),
		runner.WithToolsOverride(toolsOverride),
		runner.WithTelemetryConfig("", false, false, false, "", 0.0, nil, false, nil),
	}

	// Configure middleware from flags
	options = append(options,
		runner.WithMiddlewareFromFlags(
			nil,
			req.ToolsFilter,
			toolsOverride,
			nil,
			req.AuthzConfig,
			false,
			"",
			req.Name,
			req.Transport,
		),
	)

	runConfig, err := runner.NewRunConfigBuilder(ctx, imageMetadata, req.EnvVars, &runner.DetachedEnvVarValidator{}, options...)
	if err != nil {
		logger.Errorf("Failed to build run config: %v", err)
		return nil, fmt.Errorf("%w: Failed to build run config: %v", retriever.ErrInvalidRunConfig, err)
	}

	return runConfig, nil
}

// createRequestToRemoteAuthConfig converts API request to runner RemoteAuthConfig
func (s *WorkloadService) createRequestToRemoteAuthConfig(
	ctx context.Context,
	req *createRequest,
) (*runner.RemoteAuthConfig, error) {

	// Resolve client secret from secret management if provided
	clientSecret, err := s.resolveClientSecret(ctx, req.OAuthConfig.ClientSecret)
	if err != nil {
		return nil, err
	}

	// Create RemoteAuthConfig
	return &runner.RemoteAuthConfig{
		ClientID:     req.OAuthConfig.ClientID,
		ClientSecret: clientSecret,
		Scopes:       req.OAuthConfig.Scopes,
		Issuer:       req.OAuthConfig.Issuer,
		AuthorizeURL: req.OAuthConfig.AuthorizeURL,
		TokenURL:     req.OAuthConfig.TokenURL,
		OAuthParams:  req.OAuthConfig.OAuthParams,
		CallbackPort: req.OAuthConfig.CallbackPort,
		SkipBrowser:  req.OAuthConfig.SkipBrowser,
		Headers:      req.Headers,
	}, nil
}

// resolveClientSecret resolves client secret from secret management
func (s *WorkloadService) resolveClientSecret(ctx context.Context, secretParam *secrets.SecretParameter) (string, error) {
	var clientSecret string
	if secretParam != nil {
		// Get the secret from the secrets manager
		secretValue, err := s.secretsProvider.GetSecret(ctx, secretParam.Name)
		if err != nil {
			return "", fmt.Errorf("failed to resolve OAuth client secret: %w", err)
		}
		clientSecret = secretValue
	}
	return clientSecret, nil
}

// GetWorkloadNamesFromRequest gets workload names from either the names field or group
func (s *WorkloadService) GetWorkloadNamesFromRequest(ctx context.Context, req bulkOperationRequest) ([]string, error) {
	if len(req.Names) > 0 {
		return req.Names, nil
	}

	if err := validation.ValidateGroupName(req.Group); err != nil {
		return nil, fmt.Errorf("invalid group name: %w", err)
	}

	// Check if the group exists
	exists, err := s.groupManager.Exists(ctx, req.Group)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("group '%s' does not exist", req.Group)
	}

	// Get all workload names in the group
	workloadNames, err := s.workloadManager.ListWorkloadsInGroup(ctx, req.Group)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	return workloadNames, nil
}
