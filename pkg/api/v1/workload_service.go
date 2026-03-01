// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	regtypes "github.com/stacklok/toolhive-core/registry/types"
	groupval "github.com/stacklok/toolhive-core/validation/group"
	httpval "github.com/stacklok/toolhive-core/validation/http"
	"github.com/stacklok/toolhive/pkg/auth/remote"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/runner"
	"github.com/stacklok/toolhive/pkg/runner/retriever"
	"github.com/stacklok/toolhive/pkg/secrets"
	"github.com/stacklok/toolhive/pkg/transport"
	"github.com/stacklok/toolhive/pkg/transport/types"
	"github.com/stacklok/toolhive/pkg/workloads"
)

const (
	// imageRetrievalTimeout is the timeout for pulling Docker images
	// Set to 10 minutes to handle large images (1GB+) on slower connections
	imageRetrievalTimeout = 10 * time.Minute
)

// WorkloadService handles business logic for workload operations
type WorkloadService struct {
	workloadManager  workloads.Manager
	groupManager     groups.Manager
	containerRuntime runtime.Runtime
	debugMode        bool
	imageRetriever   retriever.Retriever
	appConfig        *config.Config
}

// NewWorkloadService creates a new WorkloadService instance
func NewWorkloadService(
	workloadManager workloads.Manager,
	groupManager groups.Manager,
	containerRuntime runtime.Runtime,
	debugMode bool,
) *WorkloadService {
	// Load application config for global settings
	configProvider := config.NewDefaultProvider()
	appConfig := configProvider.GetConfig()

	return &WorkloadService{
		workloadManager:  workloadManager,
		groupManager:     groupManager,
		containerRuntime: containerRuntime,
		debugMode:        debugMode,
		imageRetriever:   retriever.GetMCPServer,
		appConfig:        appConfig,
	}
}

// CreateWorkloadFromRequest creates a workload from a request
func (s *WorkloadService) CreateWorkloadFromRequest(ctx context.Context, req *createRequest) (*runner.RunConfig, error) {
	// Build the full run config (no existing port, so pass 0)
	runConfig, err := s.BuildFullRunConfig(ctx, req, 0)
	if err != nil {
		return nil, err
	}

	// Save the workload state
	if err := runConfig.SaveState(ctx); err != nil {
		slog.Error("failed to save workload config", "error", err)
		return nil, fmt.Errorf("failed to save workload config: %w", err)
	}

	// Start workload
	if err := s.workloadManager.RunWorkloadDetached(ctx, runConfig); err != nil {
		slog.Error("failed to start workload", "error", err)
		return nil, fmt.Errorf("failed to start workload: %w", err)
	}

	return runConfig, nil
}

// UpdateWorkloadFromRequest updates a workload from a request
func (s *WorkloadService) UpdateWorkloadFromRequest(ctx context.Context, name string, req *createRequest, existingPort int) (*runner.RunConfig, error) { //nolint:lll
	// If ProxyPort is 0, reuse the existing port
	if req.ProxyPort == 0 && existingPort > 0 {
		req.ProxyPort = existingPort
		slog.Debug("reusing existing port", "port", existingPort, "name", name)
	}

	// Build the full run config
	runConfig, err := s.BuildFullRunConfig(ctx, req, existingPort)
	if err != nil {
		return nil, fmt.Errorf("failed to build workload config: %w", err)
	}

	// Use the manager's UpdateWorkload method to handle the lifecycle
	// Use background context since this is async operation
	if _, err := s.workloadManager.UpdateWorkload(context.Background(), name, runConfig); err != nil {
		return nil, fmt.Errorf("failed to update workload: %w", err)
	}

	return runConfig, nil
}

// BuildFullRunConfig builds a complete RunConfig
//
//nolint:gocyclo // TODO: refactor this into shorter functions
func (s *WorkloadService) BuildFullRunConfig(
	ctx context.Context, req *createRequest, existingPort int,
) (*runner.RunConfig, error) {
	// Default proxy mode to streamable-http if not specified (SSE is deprecated)
	if !types.IsValidProxyMode(req.ProxyMode) {
		if req.ProxyMode == "" {
			req.ProxyMode = types.ProxyModeStreamableHTTP.String()
		} else {
			return nil, fmt.Errorf("%w: %s", retriever.ErrInvalidRunConfig, fmt.Sprintf("Invalid proxy_mode: %s", req.ProxyMode))
		}
	}

	// Validate user-provided resource indicator (RFC 8707)
	if req.OAuthConfig.Resource != "" {
		if err := httpval.ValidateResourceURI(req.OAuthConfig.Resource); err != nil {
			return nil, fmt.Errorf("%w: invalid resource parameter: %w", retriever.ErrInvalidRunConfig, err)
		}
	}

	// Validate user-provided OAuth callback port
	if req.OAuthConfig.CallbackPort != 0 {
		if err := networking.ValidateCallbackPort(req.OAuthConfig.CallbackPort, req.OAuthConfig.ClientID); err != nil {
			return nil, fmt.Errorf("%w: invalid OAuth callback port configuration", retriever.ErrInvalidRunConfig)
		}
	}

	// Validate header forward configuration
	if err := validateHeaderForwardConfig(req.HeaderForward); err != nil {
		return nil, fmt.Errorf("%w: %w", retriever.ErrInvalidRunConfig, err)
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

	var remoteAuthConfig *remote.Config
	var imageURL string
	var imageMetadata *regtypes.ImageMetadata
	var serverMetadata regtypes.ServerMetadata

	if req.URL != "" {
		// Configure remote authentication if OAuth config is provided
		if req.Transport == "" {
			req.Transport = types.TransportTypeStreamableHTTP.String()
		}
		remoteAuthConfig = createRequestToRemoteAuthConfig(ctx, req)
	} else {
		// Create a dedicated context with longer timeout for image retrieval
		imageCtx, cancel := context.WithTimeout(ctx, imageRetrievalTimeout)
		defer cancel()

		// Fetch or build the requested image
		imageURL, serverMetadata, err = s.imageRetriever(
			imageCtx,
			req.Image,
			"", // We do not let the user specify a CA cert path here.
			retriever.VerifyImageWarn,
			"",  // TODO Add support for registry groups lookups for API
			nil, // No runtime override from API (yet)
		)
		if err != nil {
			// Check if the error is due to context timeout
			if errors.Is(imageCtx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("image retrieval timed out after %v - image may be too large or connection too slow",
					imageRetrievalTimeout)
			}
			return nil, fmt.Errorf("failed to retrieve MCP server image: %w", err)
		}

		if remoteServerMetadata, ok := serverMetadata.(*regtypes.RemoteServerMetadata); ok && remoteServerMetadata != nil {
			if remoteServerMetadata.OAuthConfig != nil {
				// Default resource: user-provided > registry metadata > derived from remote URL
				resource := req.OAuthConfig.Resource
				if resource == "" {
					resource = remoteServerMetadata.OAuthConfig.Resource
				}
				if resource == "" && remoteServerMetadata.URL != "" {
					resource = remote.DefaultResourceIndicator(remoteServerMetadata.URL)
				}

				remoteAuthConfig = &remote.Config{
					ClientID:     req.OAuthConfig.ClientID,
					Scopes:       remoteServerMetadata.OAuthConfig.Scopes,
					CallbackPort: remoteServerMetadata.OAuthConfig.CallbackPort,
					Issuer:       remoteServerMetadata.OAuthConfig.Issuer,
					AuthorizeURL: remoteServerMetadata.OAuthConfig.AuthorizeURL,
					TokenURL:     remoteServerMetadata.OAuthConfig.TokenURL,
					UsePKCE:      remoteServerMetadata.OAuthConfig.UsePKCE,
					Resource:     resource,
					OAuthParams:  remoteServerMetadata.OAuthConfig.OAuthParams,
					Headers:      remoteServerMetadata.Headers,
					EnvVars:      remoteServerMetadata.EnvVars,
				}

				// Store the client secret in CLI format if provided
				if req.OAuthConfig.ClientSecret != nil {
					remoteAuthConfig.ClientSecret = req.OAuthConfig.ClientSecret.ToCLIString()
				}

				// Store the bearer token in CLI format if provided
				if req.OAuthConfig.BearerToken != nil {
					remoteAuthConfig.BearerToken = req.OAuthConfig.BearerToken.ToCLIString()
				}
			}
		}
		// Handle server metadata - API only supports container servers.
		// Use type assertion with nil check to guard against typed nil pointers.
		if md, ok := serverMetadata.(*regtypes.ImageMetadata); ok && md != nil {
			imageMetadata = md
		}
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
		runner.WithTrustProxyHeaders(req.TrustProxyHeaders),
		runner.WithK8sPodPatch(""),
		runner.WithProxyMode(types.ProxyMode(req.ProxyMode)),
		runner.WithTransportAndPorts(req.Transport, req.ProxyPort, req.TargetPort),
		runner.WithAuditEnabled(false, ""),
		runner.WithOIDCConfig(req.OIDC.Issuer, req.OIDC.Audience, req.OIDC.JwksURL, "",
			req.OIDC.ClientID, "", "", "", "", false, false, req.OIDC.Scopes),
		runner.WithToolsFilter(req.ToolsFilter),
		runner.WithToolsOverride(toolsOverride),
		runner.WithTelemetryConfigFromFlags("", false, false, false, "", 0.0, nil, false, nil, false),
	}

	// Add header forward configuration if specified
	if req.HeaderForward != nil {
		if len(req.HeaderForward.AddPlaintextHeaders) > 0 {
			options = append(options, runner.WithHeaderForward(req.HeaderForward.AddPlaintextHeaders))
		}
		if len(req.HeaderForward.AddHeadersFromSecret) > 0 {
			options = append(options, runner.WithHeaderForwardSecrets(req.HeaderForward.AddHeadersFromSecret))
		}
	}

	// Add existing port if provided (for update operations)
	if existingPort > 0 {
		options = append(options, runner.WithExistingPort(existingPort))
	}

	// Determine transport type
	transportType := "streamable-http"
	if req.Transport != "" {
		transportType = req.Transport
	} else if md, ok := serverMetadata.(*regtypes.ImageMetadata); ok && md != nil {
		if t := md.GetTransport(); t != "" {
			transportType = t
		}
	}

	// Configure middleware from flags
	options = append(options,
		runner.WithMiddlewareFromFlags(
			nil,
			nil, // tokenExchangeConfig - not supported via API yet
			req.ToolsFilter,
			toolsOverride,
			nil,
			req.AuthzConfig,
			false,
			"",
			req.Name,
			transportType,
			s.appConfig.DisableUsageMetrics,
		),
	)

	runConfig, err := runner.NewRunConfigBuilder(ctx, imageMetadata, req.EnvVars, &runner.DetachedEnvVarValidator{}, options...)
	if err != nil {
		slog.Error("failed to build run config", "error", err)
		return nil, fmt.Errorf("%w: Failed to build run config: %w", retriever.ErrInvalidRunConfig, err)
	}

	return runConfig, nil
}

// createRequestToRemoteAuthConfig converts API request to runner RemoteAuthConfig
func createRequestToRemoteAuthConfig(
	_ context.Context,
	req *createRequest,
) *remote.Config {

	// Default resource: user-provided > derived from remote URL
	resource := req.OAuthConfig.Resource
	if resource == "" && req.URL != "" {
		resource = remote.DefaultResourceIndicator(req.URL)
	}

	// Create RemoteAuthConfig
	remoteAuthConfig := &remote.Config{
		ClientID:     req.OAuthConfig.ClientID,
		Scopes:       req.OAuthConfig.Scopes,
		Issuer:       req.OAuthConfig.Issuer,
		AuthorizeURL: req.OAuthConfig.AuthorizeURL,
		TokenURL:     req.OAuthConfig.TokenURL,
		UsePKCE:      req.OAuthConfig.UsePKCE,
		Resource:     resource,
		OAuthParams:  req.OAuthConfig.OAuthParams,
		CallbackPort: req.OAuthConfig.CallbackPort,
		SkipBrowser:  req.OAuthConfig.SkipBrowser,
		Headers:      req.Headers,
	}

	// Store the client secret in CLI format if provided
	if req.OAuthConfig.ClientSecret != nil {
		remoteAuthConfig.ClientSecret = req.OAuthConfig.ClientSecret.ToCLIString()
	}

	// Store the bearer token in CLI format if provided
	if req.OAuthConfig.BearerToken != nil {
		remoteAuthConfig.BearerToken = req.OAuthConfig.BearerToken.ToCLIString()
	}

	return remoteAuthConfig
}

// GetWorkloadNamesFromRequest gets workload names from either the names field or group
func (s *WorkloadService) GetWorkloadNamesFromRequest(ctx context.Context, req bulkOperationRequest) ([]string, error) {
	if len(req.Names) > 0 {
		return req.Names, nil
	}

	if err := groupval.ValidateName(req.Group); err != nil {
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
