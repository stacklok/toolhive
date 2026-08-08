// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes/configmaps"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/oidc"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/runconfig/configmap/checksum"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
	operatorvmcpconfig "github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpconfig"
	"github.com/stacklok/toolhive/pkg/groups"
	vmcptypes "github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
	"github.com/stacklok/toolhive/pkg/vmcp/workloads"
)

// ensureVmcpConfigConfigMap ensures the vmcp Config ConfigMap exists and is up to date.
// workloadInfos is the list of workloads in the group, passed in to ensure consistency
// across multiple calls that need the same workload list.
// telemetryCfg is the already-fetched MCPTelemetryConfig (nil when not referenced),
// passed through from handleConfigRefs to avoid redundant API calls.
// statusManager is used to set auth config conditions for any conversion failures.
func (r *VirtualMCPServerReconciler) ensureVmcpConfigConfigMap(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	typedWorkloads []workloads.TypedWorkload,
	telemetryCfg *mcpv1beta1.MCPTelemetryConfig,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	// Create OIDC resolver and converter for CRD-to-config transformation
	oidcResolver := oidc.NewResolver(r.Client)
	converter, err := operatorvmcpconfig.NewConverter(oidcResolver, r.Client)
	if err != nil {
		return fmt.Errorf("failed to create vmcp converter: %w", err)
	}
	// processOutgoingAuth below is the authoritative, status-aware conversion
	// path. Defer promoted outgoing auth here so one invalid backend cannot make
	// the generic converter return before per-backend conditions are recorded and
	// the remaining valid backends are retained.
	baseVMCP := vmcp.DeepCopy()
	baseVMCP.Spec.OutgoingAuth = nil
	config, authServerRC, err := converter.Convert(ctx, baseVMCP, telemetryCfg)
	if err != nil {
		return fmt.Errorf("failed to create vmcp Config from VirtualMCPServer: %w", err)
	}
	// Clearing the promoted spec above makes the generic converter apply its
	// default source (discovered). Restore the source selected on the original
	// resource so the status-aware path retains inline semantics when requested.
	if config.OutgoingAuth != nil {
		config.OutgoingAuth.Source = outgoingAuthSource(vmcp)
	}

	// Process outgoing auth configuration for both inline and discovered modes
	if err := r.processOutgoingAuth(ctx, vmcp, config, typedWorkloads, statusManager); err != nil {
		return err
	}

	// Auto-populate optimizer config from EmbeddingServerRef or emit warnings.
	if err := r.populateOptimizerEmbeddingService(ctx, vmcp, config); err != nil {
		return err
	}

	// Validate the vmcp Config before creating the ConfigMap
	validator := operatorvmcpconfig.NewValidator()
	if err := validator.Validate(ctx, config); err != nil {
		return fmt.Errorf("invalid vmcp Config: %w", err)
	}

	// Cross-validate auth server RunConfig against backend strategies.
	// TODO: Move this into the operator's vmcpconfig.Validator wrapper so callers
	// don't need to know about the two-step validation sequence.
	if err := vmcpconfig.ValidateAuthServerIntegration(config, authServerRC); err != nil {
		message := fmt.Sprintf("invalid auth server integration: %v", err)
		statusManager.SetPhase(mcpv1beta1.VirtualMCPServerPhaseFailed)
		statusManager.SetMessage(message)
		statusManager.SetAuthServerConfigValidatedCondition(
			mcpv1beta1.ConditionReasonAuthServerConfigInvalid,
			message,
			metav1.ConditionFalse,
		)
		statusManager.SetObservedGeneration(vmcp.Generation)
		return &SpecValidationError{Message: message}
	}

	// Marshal the serializable Config to YAML for storage in ConfigMap.
	// Note: gopkg.in/yaml.v3 produces deterministic output by sorting map keys alphabetically.
	// This ensures stable checksums for triggering pod rollouts only when content actually changes.
	vmcpConfigYAML, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal vmcp config: %w", err)
	}

	configMapName := vmcpConfigMapName(vmcp.Name)
	configMapData := map[string]string{
		"config.yaml": string(vmcpConfigYAML),
	}

	// If an embedded auth server is configured, serialize its RunConfig as a separate key.
	// RunConfig contains only references (file paths, env var names) — never actual secrets —
	// so it is safe for ConfigMap storage. The vMCP binary loads this alongside config.yaml.
	if authServerRC != nil {
		authServerYAML, marshalErr := yaml.Marshal(authServerRC)
		if marshalErr != nil {
			return fmt.Errorf("failed to marshal auth server config: %w", marshalErr)
		}
		configMapData["authserver-config.yaml"] = string(authServerYAML)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: vmcp.Namespace,
			Labels:    labelsForVmcpConfig(vmcp.Name),
		},
		Data: configMapData,
	}

	// Compute and add content checksum annotation using robust SHA256-based checksum
	checksumCalculator := checksum.NewRunConfigConfigMapChecksum()
	checksumValue := checksumCalculator.ComputeConfigMapChecksum(configMap)
	configMap.Annotations = map[string]string{
		checksum.ContentChecksumAnnotation: checksumValue,
	}

	// Use the kubernetes configmaps client for upsert operations
	configMapsClient := configmaps.NewClient(r.Client, r.Scheme)
	if _, err := configMapsClient.UpsertWithOwnerReference(ctx, configMap, vmcp); err != nil {
		return fmt.Errorf("failed to upsert vmcp Config ConfigMap: %w", err)
	}

	return nil
}

// populateOptimizerEmbeddingService wires the EmbeddingServer URL into the optimizer
// config and emits warnings for non-recommended configurations.
//
// Decision matrix (ref = EmbeddingServerRef, svc = config.optimizer.embeddingService):
//
//	ref set + optimizer set + svc set → ref overrides svc (warning)
//	ref set + optimizer set + svc empty → ref populates svc (auto-configured event)
//	ref nil + optimizer set + svc set → warning: prefer embeddingServerRef
//	ref nil + optimizer set + svc empty → rejected earlier by Validate()
//
// Note: Validate() auto-populates optimizer with defaults when ref is set but optimizer is nil,
// so the "ref set + optimizer nil" case no longer reaches this function.
func (r *VirtualMCPServerReconciler) populateOptimizerEmbeddingService(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	config *vmcpconfig.Config,
) error {
	ctxLogger := log.FromContext(ctx)
	hasRef := vmcp.Spec.EmbeddingServerRef != nil

	if hasRef && config.Optimizer != nil {
		// When the optimizer has no embeddingService set, it will be auto-populated
		// from the EmbeddingServerRef URL.
		return r.populateOptimizerFromRef(ctx, vmcp, config)
	}

	// No ref — warn if the user manually set the embedding service.
	if config.Optimizer != nil && config.Optimizer.EmbeddingService != "" {
		ctxLogger.Info("config.optimizer.embeddingService is set without embeddingServerRef; "+
			"consider using embeddingServerRef for managed lifecycle",
			"embeddingService", config.Optimizer.EmbeddingService)
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, nil, corev1.EventTypeWarning, "EmbeddingServiceManual", "ValidateEmbeddingService",
				"config.optimizer.embeddingService is set without embeddingServerRef; "+
					"specifying an embeddingServerRef is the recommended configuration")
		}
	}
	return nil
}

// populateOptimizerFromRef resolves the EmbeddingServer URL and writes it into
// config.Optimizer.EmbeddingService, warning if it overrides a manually-set value.
func (r *VirtualMCPServerReconciler) populateOptimizerFromRef(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	config *vmcpconfig.Config,
) error {
	ctxLogger := log.FromContext(ctx)

	esURL, err := r.resolveEmbeddingServiceURL(ctx, vmcp)
	if err != nil {
		return fmt.Errorf("failed to resolve embedding service URL: %w", err)
	}
	if config.Optimizer.EmbeddingService != "" && esURL != "" {
		ctxLogger.Info("EmbeddingServerRef overrides config.optimizer.embeddingService",
			"ref", vmcp.Spec.EmbeddingServerRef.Name,
			"overridden", config.Optimizer.EmbeddingService,
			"new", esURL)
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, nil, corev1.EventTypeWarning, "EmbeddingServiceOverridden", "ResolveEmbeddingService",
				"config.optimizer.embeddingService will be replaced by EmbeddingServerRef %q URL",
				vmcp.Spec.EmbeddingServerRef.Name)
		}
	}
	if esURL != "" {
		config.Optimizer.EmbeddingService = esURL
	}
	return nil
}

// labelsForVmcpConfig returns labels for vmcp config ConfigMap
func labelsForVmcpConfig(vmcpName string) map[string]string {
	return map[string]string{
		"toolhive.stacklok.io/component":          "vmcp-config",
		"toolhive.stacklok.io/virtual-mcp-server": vmcpName,
		"toolhive.stacklok.io/managed-by":         "toolhive-operator",
	}
}

// discoverBackendsWithMetadata discovers backends and returns full Backend objects with metadata.
// Used in static mode for ConfigMap generation to preserve backend metadata.
func (r *VirtualMCPServerReconciler) discoverBackendsWithMetadata(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
) ([]vmcptypes.Backend, error) {
	groupsManager := groups.NewCRDManager(r.Client, vmcp.Namespace)
	workloadDiscoverer := workloads.NewK8SDiscovererWithClient(r.Client, vmcp.Namespace)

	// Build auth config if OutgoingAuth is configured
	var authConfig *vmcpconfig.OutgoingAuthConfig
	if vmcp.Spec.OutgoingAuth != nil {
		typedWorkloads, err := workloadDiscoverer.ListWorkloadsInGroup(ctx, vmcp.ResolveGroupName())
		if err != nil {
			return nil, fmt.Errorf("failed to list workloads in group: %w", err)
		}

		// Build auth config and collect per-backend errors. Inventory errors are
		// transient and must abort before an incomplete auth view is used.
		// Note: Auth errors are collected and reported via status conditions by processOutgoingAuth.
		// In static mode, we still attempt to build the auth config for ConfigMap embedding.
		authConfig, _, _, err = r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
		if err != nil {
			return nil, fmt.Errorf("failed to build outgoing auth config: %w", err)
		}

		// Carry the resolved override marker into workload conversion as well as
		// the aggregator. Otherwise an unsupported backend ref can fail closed
		// before the aggregator gets a chance to apply the valid override.
		workloadDiscoverer = workloads.NewK8SDiscovererWithClientAndAuthConfig(
			r.Client,
			vmcp.Namespace,
			authConfig,
		)
	}

	backendDiscoverer := aggregator.NewUnifiedBackendDiscoverer(workloadDiscoverer, groupsManager, authConfig)
	backends, err := backendDiscoverer.Discover(ctx, vmcp.ResolveGroupName())
	if err != nil {
		return nil, fmt.Errorf("failed to discover backends: %w", err)
	}

	return backends, nil
}

// buildTransportMap builds a map of backend names to transport types from workload Specs.
// Used in static mode to populate transport field in ConfigMap.
func (r *VirtualMCPServerReconciler) buildTransportMap(
	ctx context.Context,
	namespace string,
	typedWorkloads []workloads.TypedWorkload,
) (map[string]string, error) {
	transportMap := make(map[string]string, len(typedWorkloads))

	mcpServerMap, err := r.listMCPServersAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServers: %w", err)
	}

	mcpRemoteProxyMap, err := r.listMCPRemoteProxiesAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPRemoteProxies: %w", err)
	}

	mcpServerEntryMap, err := r.listMCPServerEntriesAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServerEntries: %w", err)
	}

	for _, workload := range typedWorkloads {
		var transport string

		switch workload.Type {
		case workloads.WorkloadTypeMCPServer:
			if mcpServer, found := mcpServerMap[workload.Name]; found {
				// Read effective transport (ProxyMode takes precedence over Transport)
				// For stdio servers, ProxyMode indicates how they're proxied (sse or streamable-http)
				if mcpServer.Spec.ProxyMode != "" {
					transport = string(mcpServer.Spec.ProxyMode)
				} else {
					transport = string(mcpServer.Spec.Transport)
				}
			}

		case workloads.WorkloadTypeMCPRemoteProxy:
			if mcpRemoteProxy, found := mcpRemoteProxyMap[workload.Name]; found {
				transport = string(mcpRemoteProxy.Spec.Transport)
			}

		case workloads.WorkloadTypeMCPServerEntry:
			if mcpServerEntry, found := mcpServerEntryMap[workload.Name]; found {
				transport = mcpServerEntry.Spec.Transport
			}
		}

		if transport != "" {
			transportMap[workload.Name] = transport
		}
	}

	return transportMap, nil
}

// buildCABundlePathMap builds a map of backend names to CA bundle file paths for MCPServerEntry backends.
// Only entries with a caBundleRef are included in the map.
func (r *VirtualMCPServerReconciler) buildCABundlePathMap(
	ctx context.Context,
	namespace string,
	typedWorkloads []workloads.TypedWorkload,
) (map[string]string, error) {
	caBundlePathMap := make(map[string]string)

	// Early return if no MCPServerEntry workloads to avoid unnecessary API calls
	hasEntries := false
	for _, workload := range typedWorkloads {
		if workload.Type == workloads.WorkloadTypeMCPServerEntry {
			hasEntries = true
			break
		}
	}
	if !hasEntries {
		return caBundlePathMap, nil
	}

	mcpServerEntryMap, err := r.listMCPServerEntriesAsMap(ctx, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to list MCPServerEntries: %w", err)
	}

	for _, workload := range typedWorkloads {
		if workload.Type != workloads.WorkloadTypeMCPServerEntry {
			continue
		}
		entry, found := mcpServerEntryMap[workload.Name]
		if !found || entry.Spec.CABundleRef == nil || entry.Spec.CABundleRef.ConfigMapRef == nil {
			continue
		}
		caBundlePathMap[workload.Name] = caBundleMountPath(workload.Name, entry.Spec.CABundleRef)
	}

	return caBundlePathMap, nil
}

// extractInlineBackendNames extracts concrete per-backend overrides from the
// VirtualMCPServer spec. A type="discovered" entry is a discovery directive,
// not an inline auth configuration, so it must not own a BackendAuthConfig-*
// status condition.
func extractInlineBackendNames(vmcp *mcpv1beta1.VirtualMCPServer) []string {
	if vmcp.Spec.OutgoingAuth == nil || vmcp.Spec.OutgoingAuth.Backends == nil {
		return nil
	}
	names := make([]string, 0, len(vmcp.Spec.OutgoingAuth.Backends))
	for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		if backendAuth.Type == mcpv1beta1.BackendAuthTypeDiscovered {
			continue
		}
		names = append(names, backendName)
	}
	sort.Strings(names)
	return names
}

// backendsWithFailedAuth returns the set of backend names whose outgoing auth strategy
// failed to build (conversion error, mirrored-invalid source, or subject-provider
// injection error). These backends must never be served: since ResolveForBackend falls
// through to Default for any backend absent from OutgoingAuthConfig.Backends, silently
// omitting a failed backend from that map is not enough to keep it from being routed to
// with the wrong (Default) identity — it must also be dropped from the served backend set.
//
// Default-only errors (empty BackendName) are handled separately by
// excludeDefaultDependentBackends because they apply to every workload that lacks a
// successfully resolved backend-specific strategy.
//
// A backend name recorded in authErrors is only added to the exclusion set if it also
// lacks a successfully resolved strategy in resolvedBackends. This is a defense-in-depth
// guard for cases where multiple configuration paths produce results for the same backend:
// a valid resolved strategy must not be made unroutable by an unrelated error.
func backendsWithFailedAuth(
	authErrors []AuthConfigError,
	resolvedBackends map[string]*authtypes.BackendAuthStrategy,
) map[string]struct{} {
	failed := make(map[string]struct{})
	for _, authErr := range authErrors {
		if authErr.BackendName == "" {
			continue
		}
		if resolvedBackends[authErr.BackendName] != nil {
			continue
		}
		failed[authErr.BackendName] = struct{}{}
	}
	return failed
}

// excludeDefaultDependentBackends extends excluded with every workload that would
// otherwise depend on an explicitly configured Default strategy that failed to build.
// A valid backend-specific strategy remains routable, so one invalid default does not
// unnecessarily take healthy peers offline.
func excludeDefaultDependentBackends(
	excluded map[string]struct{},
	authErrors []AuthConfigError,
	resolvedBackends map[string]*authtypes.BackendAuthStrategy,
	typedWorkloads []workloads.TypedWorkload,
) {
	defaultFailed := false
	for _, authErr := range authErrors {
		if authErr.Context == authContextDefault {
			defaultFailed = true
			break
		}
	}
	if !defaultFailed {
		return
	}

	for _, workload := range typedWorkloads {
		if resolvedBackends[workload.Name] == nil {
			excluded[workload.Name] = struct{}{}
		}
	}
}

func sortedBackendNames(backends map[string]struct{}) []string {
	if len(backends) == 0 {
		return nil
	}

	names := make([]string, 0, len(backends))
	for name := range backends {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// determineValidInlineBackends determines which concrete per-backend overrides
// resolved successfully. inlineBackendNames is already filtered to exclude
// type="discovered" directives.
func determineValidInlineBackends(authConfig *vmcpconfig.OutgoingAuthConfig, inlineBackendNames []string) []string {
	if authConfig == nil || authConfig.Backends == nil {
		return nil
	}
	valid := make([]string, 0, len(inlineBackendNames))
	for _, backendName := range inlineBackendNames {
		if authConfig.Backends[backendName] != nil {
			valid = append(valid, backendName)
		}
	}
	return valid
}

// determineExplicitBackendOverrides returns successfully resolved concrete
// per-backend overrides. A type="discovered" entry remains a discovery
// directive and is intentionally omitted: its unauthenticated runtime strategy
// is only a fallback when the backend does not expose discovered auth.
//
// The returned slice is non-nil and sorted so it is serialized deterministically
// even when no concrete overrides are configured. This lets the runtime
// distinguish operator-resolved discovered strategies from explicit overrides.
func determineExplicitBackendOverrides(
	vmcp *mcpv1beta1.VirtualMCPServer,
	authConfig *vmcpconfig.OutgoingAuthConfig,
) []string {
	explicit := make([]string, 0)
	if vmcp.Spec.OutgoingAuth == nil || authConfig == nil {
		return explicit
	}

	for backendName, backendAuth := range vmcp.Spec.OutgoingAuth.Backends {
		if backendAuth.Type == mcpv1beta1.BackendAuthTypeDiscovered {
			continue
		}
		if authConfig.Backends[backendName] != nil {
			explicit = append(explicit, backendName)
		}
	}
	sort.Strings(explicit)
	return explicit
}

// processOutgoingAuth processes outgoing auth configuration for both inline and discovered modes.
// It builds auth configs, sets status conditions for all auth config types, and configures static backends for inline mode.
func (r *VirtualMCPServerReconciler) processOutgoingAuth(
	ctx context.Context,
	vmcp *mcpv1beta1.VirtualMCPServer,
	config *vmcpconfig.Config,
	typedWorkloads []workloads.TypedWorkload,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	// Clean up stale conditions if outgoing auth is not configured
	if config.OutgoingAuth == nil {
		setAuthConfigConditions(statusManager, nil, nil, false, nil, nil)
		return nil
	}

	isInlineMode := config.OutgoingAuth.Source == OutgoingAuthSourceInline
	isDiscoveredMode := config.OutgoingAuth.Source == OutgoingAuthSourceDiscovered

	// Clean up stale conditions if not using inline or discovered mode
	if !isInlineMode && !isDiscoveredMode {
		setAuthConfigConditions(statusManager, nil, nil, false, nil, nil)
		return nil
	}

	// Build auth config and collect all per-config errors (default, backend-specific, discovered).
	// A transient inventory error aborts reconciliation before status or ConfigMap state is persisted.
	authConfig, backendsWithAuthConfig, allAuthErrors, err := r.buildOutgoingAuthConfig(ctx, vmcp, typedWorkloads)
	if err != nil {
		return fmt.Errorf("failed to build outgoing auth config: %w", err)
	}

	// Backends whose auth strategy failed to build must be excluded from the served
	// set entirely — see backendsWithFailedAuth for why omission from
	// authConfig.Backends alone is not sufficient.
	var resolvedBackends map[string]*authtypes.BackendAuthStrategy
	if authConfig != nil {
		resolvedBackends = authConfig.Backends
	}
	excludedBackends := backendsWithFailedAuth(allAuthErrors, resolvedBackends)
	excludeDefaultDependentBackends(excludedBackends, allAuthErrors, resolvedBackends, typedWorkloads)

	// Extract inline backend names and determine valid auth configs
	inlineBackendNames := extractInlineBackendNames(vmcp)
	hasValidDefaultAuth := authConfig != nil && authConfig.Default != nil
	validInlineBackends := determineValidInlineBackends(authConfig, inlineBackendNames)

	// Set conditions for all auth config types (default, backend-specific, discovered)
	// True for success, False for errors
	setAuthConfigConditions(
		statusManager,
		backendsWithAuthConfig,
		inlineBackendNames,
		hasValidDefaultAuth,
		validInlineBackends,
		allAuthErrors,
	)

	// Persist only successfully resolved strategies plus the deterministic deny
	// list. Dynamic discovery consumes ExcludedBackends before a workload can be
	// converted or routed; static conversion applies the same set below.
	if authConfig != nil {
		authConfig.ExcludedBackends = sortedBackendNames(excludedBackends)
		config.OutgoingAuth = authConfig
	}

	// Static mode (inline): Embed full backend details in ConfigMap
	if isInlineMode {
		// Discover backends with metadata
		backends, err := r.discoverBackendsWithMetadata(ctx, vmcp)
		if err != nil {
			return fmt.Errorf("failed to discover backends for static mode: %w", err)
		}

		// Get transport types from workload specs
		transportMap, err := r.buildTransportMap(ctx, vmcp.Namespace, typedWorkloads)
		if err != nil {
			return fmt.Errorf("failed to build transport map for static mode: %w", err)
		}

		// Build CA bundle path map for MCPServerEntry backends
		caBundlePathMap, err := r.buildCABundlePathMap(ctx, vmcp.Namespace, typedWorkloads)
		if err != nil {
			return fmt.Errorf("failed to build CA bundle path map for static mode: %w", err)
		}

		config.Backends = convertBackendsToStaticBackends(ctx, backends, transportMap, caBundlePathMap, excludedBackends)

		// Validate at least one backend exists
		if len(config.Backends) == 0 {
			return fmt.Errorf(
				"static mode requires at least one backend with valid transport (%v), "+
					"but none were discovered in group %s",
				vmcpconfig.StaticModeAllowedTransports,
				config.Group,
			)
		}
	}
	// Dynamic mode (discovered): vMCP discovers backends at runtime via K8s API.
	// The sanitized strategies and deny list above are required so a failed
	// explicit strategy cannot silently degrade to a different identity or no auth.

	return nil
}
