// Package aggregator provides platform-specific backend discovery implementations.
//
// This file contains:
//   - Unified backend discoverer implementation (works with both CLI and Kubernetes)
//   - Platform-specific factory functions (NewK8SBackendDiscoverer, NewCLIBackendDiscoverer)
//   - Generic factory function (NewBackendDiscoverer) that delegates to platform-specific factories
//   - WorkloadDiscoverer interface and implementations are in pkg/vmcp/workloads
//
// The BackendDiscoverer interface is defined in aggregator.go.
package aggregator

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/env"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/k8s"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/resolver"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
	workloadsmgr "github.com/stacklok/toolhive/pkg/workloads"
)

const (
	// CLIAuthConfigDirEnv is the environment variable for overriding the CLI auth config directory.
	// If set and non-empty, this value is used instead of the default XDG-based path.
	CLIAuthConfigDirEnv = "TOOLHIVE_VMCP_AUTH_CONFIG_DIR"
)

// backendDiscoverer discovers backend MCP servers using a WorkloadDiscoverer.
// This is a unified discoverer that works with both CLI and Kubernetes workloads.
type backendDiscoverer struct {
	workloadsManager workloads.Discoverer
	groupsManager    groups.Manager
	authConfig       *config.OutgoingAuthConfig
	authResolver     resolver.AuthResolver
}

// BackendDiscovererOption is a functional option for configuring a backendDiscoverer.
type BackendDiscovererOption func(*backendDiscoverer)

// WithAuthResolver sets a custom auth resolver for resolving external_auth_config_ref.
// If not set, external_auth_config_ref strategies will fail with "auth resolver not initialized".
func WithAuthResolver(authResolver resolver.AuthResolver) BackendDiscovererOption {
	return func(d *backendDiscoverer) {
		d.authResolver = authResolver
	}
}

// NewUnifiedBackendDiscoverer creates a unified backend discoverer that works with both
// CLI and Kubernetes workloads through the WorkloadDiscoverer interface.
//
// The authConfig parameter configures authentication for discovered backends.
// If nil, backends will have no authentication configured.
//
// Optional functional options can be provided to customize the discoverer:
//   - WithAuthResolver: Sets a custom auth resolver for external_auth_config_ref resolution
func NewUnifiedBackendDiscoverer(
	workloadsManager workloads.Discoverer,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
	opts ...BackendDiscovererOption,
) BackendDiscoverer {
	d := &backendDiscoverer{
		workloadsManager: workloadsManager,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// NewBackendDiscoverer creates a unified BackendDiscoverer based on the runtime environment.
// It automatically detects whether to use CLI (Docker/Podman) or Kubernetes workloads
// and creates the appropriate WorkloadDiscoverer implementation.
//
// Parameters:
//   - ctx: Context for creating managers
//   - groupsManager: Manager for group operations (must already be initialized)
//   - authConfig: Outgoing authentication configuration for discovered backends
//
// Returns:
//   - BackendDiscoverer: A unified discoverer that works with both CLI and Kubernetes workloads
//   - error: If manager creation fails
func NewBackendDiscoverer(
	ctx context.Context,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) (BackendDiscoverer, error) {
	if rt.IsKubernetesRuntime() {
		return NewK8SBackendDiscoverer(groupsManager, authConfig)
	}
	return NewCLIBackendDiscoverer(ctx, groupsManager, authConfig)
}

// NewK8SBackendDiscoverer creates a BackendDiscoverer for Kubernetes environments.
// It creates a K8s client and scheme once, then uses them for both workload discovery
// and auth resolution.
//
// Parameters:
//   - groupsManager: Manager for group operations (must already be initialized)
//   - authConfig: Outgoing authentication configuration for discovered backends
//
// Returns:
//   - BackendDiscoverer: A discoverer configured for Kubernetes workloads
//   - error: If K8s client or workload discoverer creation fails
func NewK8SBackendDiscoverer(
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) (BackendDiscoverer, error) {
	// Create a scheme for controller-runtime client
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add client-go scheme: %w", err)
	}
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add MCP v1alpha1 scheme: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := k8s.NewControllerRuntimeClient(scheme)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Get the namespace
	namespace := k8s.GetCurrentNamespace()

	// Create workload discoverer with the shared client
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(k8sClient, namespace)

	// Create auth resolver with the same client
	authResolver := resolver.NewK8SAuthResolver(k8sClient, namespace)

	return &backendDiscoverer{
		workloadsManager: workloadDiscoverer,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
		authResolver:     authResolver,
	}, nil
}

// NewK8SBackendDiscovererWithClient creates a BackendDiscoverer for Kubernetes environments
// using a pre-existing controller-runtime client. This is useful when the caller already
// has a K8s client (e.g., in the operator controller).
//
// Parameters:
//   - k8sClient: An existing controller-runtime client
//   - namespace: The namespace to operate in
//   - groupsManager: Manager for group operations (must already be initialized)
//   - authConfig: Outgoing authentication configuration for discovered backends
//
// Returns:
//   - BackendDiscoverer: A discoverer configured for Kubernetes workloads
func NewK8SBackendDiscovererWithClient(
	k8sClient client.Client,
	namespace string,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) BackendDiscoverer {
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(k8sClient, namespace)
	authResolver := resolver.NewK8SAuthResolver(k8sClient, namespace)

	return &backendDiscoverer{
		workloadsManager: workloadDiscoverer,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
		authResolver:     authResolver,
	}
}

// NewCLIBackendDiscoverer creates a BackendDiscoverer for CLI environments.
// It creates a CLI workload manager and CLI auth resolver that loads external
// auth configs from YAML files and resolves secrets from environment variables.
//
// Parameters:
//   - ctx: Context for creating the workload manager
//   - groupsManager: Manager for group operations (must already be initialized)
//   - authConfig: Outgoing authentication configuration for discovered backends
//
// Returns:
//   - BackendDiscoverer: A discoverer configured for CLI workloads
//   - error: If workload manager creation fails
func NewCLIBackendDiscoverer(
	ctx context.Context,
	groupsManager groups.Manager,
	authConfig *config.OutgoingAuthConfig,
) (BackendDiscoverer, error) {
	// Create CLI workload manager
	workloadDiscoverer, err := workloadsmgr.NewManager(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create workload manager: %w", err)
	}

	// Create CLI auth resolver that loads from configurable directory
	envReader := &env.OSReader{}
	configDir := getCLIAuthConfigDir(envReader)
	authResolver := resolver.NewCLIAuthResolver(envReader, configDir)

	return &backendDiscoverer{
		workloadsManager: workloadDiscoverer,
		groupsManager:    groupsManager,
		authConfig:       authConfig,
		authResolver:     authResolver,
	}, nil
}

// getCLIAuthConfigDir returns the directory for CLI external auth config files.
// If TOOLHIVE_VMCP_AUTH_CONFIG_DIR is set and non-empty, it uses that value.
// Otherwise, it defaults to ~/.config/toolhive/vmcp/auth-configs/ (XDG-based).
func getCLIAuthConfigDir(envReader env.Reader) string {
	if dir := strings.TrimSpace(envReader.Getenv(CLIAuthConfigDirEnv)); dir != "" {
		return dir
	}
	return filepath.Join(xdg.ConfigHome, "toolhive", "vmcp", "auth-configs")
}

// Discover finds all backend workloads in the specified group.
// Returns all accessible backends with their health status marked based on workload status.
// The groupRef is the group name (e.g., "engineering-team").
func (d *backendDiscoverer) Discover(ctx context.Context, groupRef string) ([]vmcp.Backend, error) {
	logger.Infof("Discovering backends in group %s", groupRef)

	// Verify that the group exists
	exists, err := d.groupsManager.Exists(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to check if group exists: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("group %s not found", groupRef)
	}

	// Get all workload names in the group
	workloadNames, err := d.workloadsManager.ListWorkloadsInGroup(ctx, groupRef)
	if err != nil {
		return nil, fmt.Errorf("failed to list workloads in group: %w", err)
	}

	if len(workloadNames) == 0 {
		logger.Infof("No workloads found in group %s", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Debugf("Found %d workloads in group %s, discovering backends", len(workloadNames), groupRef)

	// Query each workload and convert to backend
	var backends []vmcp.Backend
	for _, name := range workloadNames {
		backend, err := d.workloadsManager.GetWorkloadAsVMCPBackend(ctx, name)
		if err != nil {
			logger.Warnf("Failed to get workload %s: %v, skipping", name, err)
			continue
		}

		// Skip workloads that are not accessible (GetWorkload returns nil)
		if backend == nil {
			continue
		}

		// Apply authentication configuration to backend
		// If auth is explicitly configured but fails to resolve, skip this backend (fail-closed)
		if err := d.applyAuthConfigToBackend(ctx, backend, name); err != nil {
			logger.Warnf("Backend %s excluded due to auth configuration error: %v", name, err)
			continue
		}

		// Set group metadata (override user labels to prevent conflicts)
		if backend.Metadata == nil {
			backend.Metadata = make(map[string]string)
		}
		backend.Metadata["group"] = groupRef

		backends = append(backends, *backend)
	}

	if len(backends) == 0 {
		logger.Infof("No accessible backends found in group %s (all workloads lack URLs)", groupRef)
		return []vmcp.Backend{}, nil
	}

	logger.Infof("Discovered %d backends in group %s", len(backends), groupRef)
	return backends, nil
}

// applyAuthConfigToBackend applies authentication configuration to a backend based on the source mode.
// It determines whether to use discovered auth from the MCPServer or auth from the vMCP config.
//
// Auth resolution logic:
// - "discovered" mode: Use discovered auth if available, otherwise fall back to Default or backend-specific config
// - "inline" mode (or ""): Always use config-based auth, ignore discovered auth
// - unknown mode: Default to config-based auth for safety
//
// When useDiscoveredAuth is false, ResolveForBackend is called which handles:
// 1. Backend-specific config (d.authConfig.Backends[backendName])
// 2. Default config fallback (d.authConfig.Default)
// 3. No auth if neither is configured
//
// After resolving the auth config, if the type is "external_auth_config_ref", it will be
// resolved at runtime to a concrete strategy using the AuthResolver.
//
// Returns an error if auth is explicitly configured but fails to resolve (fail-closed behavior).
// This ensures backends are not accessible without authentication when auth was intended.
func (d *backendDiscoverer) applyAuthConfigToBackend(ctx context.Context, backend *vmcp.Backend, backendName string) error {
	if d.authConfig == nil {
		return nil
	}

	// Determine if we should use discovered auth or config-based auth
	var useDiscoveredAuth bool
	switch d.authConfig.Source {
	case "discovered":
		// In discovered mode, use auth discovered from MCPServer (if any exists)
		// If no auth is discovered, fall back to config-based auth via ResolveForBackend
		// which will use backend-specific config, then Default, then no auth
		useDiscoveredAuth = backend.AuthConfig != nil
	case "inline", "":
		// For inline mode or empty source, always use config-based auth
		// Ignore any discovered auth from backends
		useDiscoveredAuth = false
	default:
		// Unknown source mode - default to config-based auth for safety
		logger.Warnf("Unknown auth source mode: %s, defaulting to config-based auth", d.authConfig.Source)
		useDiscoveredAuth = false
	}

	if useDiscoveredAuth {
		// Keep the auth discovered from MCPServer (already populated in backend)
		logger.Debugf("Backend %s using discovered auth strategy: %s", backendName, backend.AuthConfig.Type)
	} else {
		// Use auth from config (inline mode)
		authConfig := d.authConfig.ResolveForBackend(backendName)
		if authConfig != nil {
			// Resolve external_auth_config_ref type to concrete strategy at runtime
			resolvedConfig, err := d.resolveExternalAuthConfigRef(ctx, authConfig, backendName)
			if err != nil {
				// Fail closed: if auth was explicitly configured but failed to resolve,
				// return an error to exclude this backend from discovery
				return fmt.Errorf("auth resolution failed: %w", err)
			}
			backend.AuthConfig = resolvedConfig
			if resolvedConfig != nil {
				logger.Debugf("Backend %s configured with auth strategy from config: %s", backendName, resolvedConfig.Type)
			}
		}
	}
	return nil
}

// resolveExternalAuthConfigRef resolves an external_auth_config_ref strategy type
// to a concrete strategy (token_exchange or header_injection) at runtime.
//
// In Kubernetes mode, it looks up MCPExternalAuthConfig CRDs and resolves secrets from K8s Secrets.
// In CLI mode, it loads YAML files from ~/.toolhive/vmcp/auth-configs/ and resolves secrets from env vars.
//
// Returns the original authConfig unchanged if:
// - authConfig is nil
// - authConfig.Type is not "external_auth_config_ref"
//
// Returns an error if:
// - authResolver is nil (should not happen with proper initialization)
// - resolution fails (config not found, secret resolution failed, etc.)
//
// This implements fail-closed behavior: if auth is explicitly configured but cannot be
// resolved, an error is returned so the backend can be excluded from discovery.
func (d *backendDiscoverer) resolveExternalAuthConfigRef(
	ctx context.Context,
	authConfig *authtypes.BackendAuthStrategy,
	backendName string,
) (*authtypes.BackendAuthStrategy, error) {
	if authConfig == nil {
		return nil, nil
	}

	// Only resolve external_auth_config_ref types
	if authConfig.Type != authtypes.StrategyTypeExternalAuthConfigRef {
		return authConfig, nil
	}

	// Check if auth resolver is available (should always be set after proper initialization)
	if d.authResolver == nil {
		return nil, fmt.Errorf("external_auth_config_ref type for backend %s cannot be resolved: "+
			"auth resolver not initialized", backendName)
	}

	// Resolve the external auth config reference
	resolved, err := d.authResolver.ResolveExternalAuthConfig(ctx, authConfig.ExternalAuthConfigRefName)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve external auth config %q for backend %s: %w",
			authConfig.ExternalAuthConfigRefName, backendName, err)
	}

	logger.Debugf("Resolved external_auth_config_ref %s to %s strategy for backend %s",
		authConfig.ExternalAuthConfigRefName, resolved.Type, backendName)
	return resolved, nil
}
