// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package usage

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/versions"
)

// Cloud provider tokens. These are the stable lowercase values reported in the
// cloud_provider column; "unknown" is the fallback when detection is
// unavailable or the provider scheme is unrecognized.
const (
	cloudProviderAWS     = "aws"
	cloudProviderGCP     = "gcp"
	cloudProviderAzure   = "azure"
	cloudProviderKind    = "kind"
	cloudProviderUnknown = "unknown"
)

// Reporter is a manager.Runnable that periodically collects an anonymous usage
// Snapshot and POSTs it to ClickHouse. It is leader-only so HA replicas do not
// double-emit.
//
// Every runtime error (identity, list, HTTP) is logged and never fatal, and the
// per-tick body is wrapped in a recover() so a panic can never crash or stall
// the manager.
type Reporter struct {
	client        client.Client
	cfg           Config
	namespace     string
	k8sVersion    string
	cloudProvider string
	featureGates  map[string]uint8
	transport     *clickHouseClient
	operatorVer   string
}

// NewReporter constructs a usage Reporter using the manager's CACHED client for
// listing MCPServer and MCPExternalAuthConfig resources. Both the k8sVersion and
// cloudProvider are resolved once at startup (see DiscoverK8sServerVersion and
// DiscoverCloudProvider) and cached for the lifetime of the reporter; an empty
// k8sVersion is acceptable and maps to the ClickHouse column default, and an
// empty/"unknown" cloudProvider is acceptable. Feature gates are read from the
// operator's feature-flag env vars once here (env does not change at runtime)
// and reused for every tick.
//
// The injected client provides cached, namespace-scoped reads consistent with
// the rest of the operator; it does NOT provide cross-replica coordination —
// that is handled by leader election (NeedsLeaderElection returns true).
func NewReporter(c client.Client, cfg Config, namespace, k8sVersion, cloudProvider string) *Reporter {
	return &Reporter{
		client:        c,
		cfg:           cfg,
		namespace:     namespace,
		k8sVersion:    k8sVersion,
		cloudProvider: cloudProvider,
		featureGates:  collectFeatureGates(),
		transport:     newClickHouseClient(cfg.ClickHouseURL),
		operatorVer:   versions.GetVersionInfo().Version,
	}
}

// Start runs the reporter until ctx is cancelled. It resolves the installation
// ID, runs an initial report immediately, then reports on the configured
// interval. Start returns nil on clean shutdown; it never returns a runtime
// error, so a reporting failure can never bring down the manager.
func (r *Reporter) Start(ctx context.Context) error {
	logger := log.FromContext(ctx).WithName("usage-reporter")

	if !r.cfg.Enabled() {
		// Defensive: app.go should skip Add when disabled, but no-op safely.
		logger.V(1).Info("usage reporter disabled, not starting")
		<-ctx.Done()
		return nil
	}

	logger.Info("Leader elected, starting usage reporter", "interval", r.cfg.ReportInterval)

	installationID, err := resolveInstallationID(ctx, r.client, r.namespace)
	if err != nil {
		// Non-fatal: log and keep the runnable alive so leadership is retained
		// and a future restart can retry. We simply have nothing to report.
		logger.Error(err, "failed to resolve installation ID; usage reporting inactive")
		<-ctx.Done()
		return nil
	}

	ticker := time.NewTicker(r.cfg.ReportInterval)
	defer ticker.Stop()

	r.reportOnce(ctx, logger, installationID)
	for {
		select {
		case <-ctx.Done():
			logger.Info("Leadership lost or shutting down, usage reporter stopped")
			return nil
		case <-ticker.C:
			r.reportOnce(ctx, logger, installationID)
		}
	}
}

// NeedsLeaderElection reports that this runnable runs only on the elected
// leader, mirroring the existing telemetry runnable so HA replicas do not
// double-emit usage events.
func (*Reporter) NeedsLeaderElection() bool {
	return true
}

// DiscoverK8sServerVersion builds a discovery client from the manager's rest
// config and returns the Kubernetes API server GitVersion. This is intended to
// be called ONCE at startup; the result should be cached and passed to
// NewReporter. An error is returned (not fatal) so the caller can log it and
// proceed with an empty version.
func DiscoverK8sServerVersion(cfg *rest.Config) (string, error) {
	dc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("build discovery client: %w", err)
	}
	info, err := dc.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("query kubernetes server version: %w", err)
	}
	return info.GitVersion, nil
}

// DiscoverCloudProvider best-effort detects the underlying cloud provider by
// listing nodes and mapping the first node's Spec.ProviderID scheme prefix to a
// stable lowercase token ("aws"/"gcp"/"azure"/"kind"); anything else, an empty
// provider ID, or no nodes maps to "unknown".
//
// This is intended to be called ONCE at startup; the result should be cached and
// passed to NewReporter (it is NOT re-detected per tick).
//
// IMPORTANT: live cloud detection requires node-read (list) RBAC, which the
// operator may NOT have. This is fine — on ANY error (RBAC forbidden, transport
// failure, no nodes) it returns ("unknown", err) so the caller can log
// non-fatally and proceed with "unknown".
func DiscoverCloudProvider(cfg *rest.Config) (string, error) {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return cloudProviderUnknown, fmt.Errorf("build kubernetes clientset: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), httpTimeout)
	defer cancel()

	// Limit to a single node — we only need the provider ID scheme, which is
	// uniform across a cluster's nodes.
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return cloudProviderUnknown, fmt.Errorf("list nodes for cloud provider detection: %w", err)
	}
	if len(nodes.Items) == 0 {
		return cloudProviderUnknown, nil
	}
	return cloudProviderFromProviderID(nodes.Items[0].Spec.ProviderID), nil
}

// cloudProviderFromProviderID maps a node Spec.ProviderID (e.g.
// "aws:///us-east-1a/i-0abc") to a stable lowercase cloud token, falling back to
// "unknown" for empty or unrecognized schemes.
func cloudProviderFromProviderID(providerID string) string {
	scheme, _, found := strings.Cut(providerID, "://")
	if !found {
		return cloudProviderUnknown
	}
	switch scheme {
	case "aws":
		return cloudProviderAWS
	case "gce":
		return cloudProviderGCP
	case "azure":
		return cloudProviderAzure
	case "kind":
		return cloudProviderKind
	default:
		return cloudProviderUnknown
	}
}

// reportOnce collects one snapshot and sends it. It recovers from any panic so
// a single bad tick can never crash or stall the manager.
func (r *Reporter) reportOnce(ctx context.Context, logger logr.Logger, installationID string) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error(fmt.Errorf("usage report panic: %v", rec), "recovered from panic during usage report")
		}
	}()

	count, err := r.countMCPServers(ctx)
	if err != nil {
		logger.Error(err, "failed to list MCPServers; skipping this usage report")
		return
	}

	// External-auth config count is best-effort: a list failure is logged and
	// treated as 0 for this tick rather than skipping the whole report.
	authCount, err := r.countMCPExternalAuthConfigs(ctx)
	if err != nil {
		logger.Error(err, "failed to list MCPExternalAuthConfigs; reporting count as 0 this tick")
		authCount = 0
	}

	snapshot := newSnapshot(
		installationID, r.operatorVer, r.k8sVersion,
		count, authCount, r.cloudProvider, r.featureGates,
	)
	if err := r.transport.send(ctx, snapshot); err != nil {
		logger.Error(err, "failed to send usage snapshot to ClickHouse")
		return
	}
	logger.V(1).Info("usage snapshot reported",
		"mcpserver_count", count,
		"mcpexternalauthconfig_count", authCount,
		"cloud_provider", r.cloudProvider,
	)
}

// countMCPServers lists MCPServer resources via the cached client. When the
// operator is namespace-scoped the cache is already limited to watched
// namespaces; the explicit namespace scoping keeps the count consistent with
// the operator's view.
func (r *Reporter) countMCPServers(ctx context.Context) (uint32, error) {
	list := &mcpv1beta1.MCPServerList{}
	opts := []client.ListOption{}
	if r.namespace != "" {
		opts = append(opts, client.InNamespace(r.namespace))
	}
	if err := r.client.List(ctx, list, opts...); err != nil {
		return 0, fmt.Errorf("list MCPServers: %w", err)
	}
	return clampCountUint32(len(list.Items)), nil
}

// countMCPExternalAuthConfigs lists MCPExternalAuthConfig resources via the
// cached client, scoped to the same namespace as countMCPServers. The count is a
// proxy for external-auth (token exchange) adoption. A list error is returned to
// the caller, which logs it and reports the count as 0 for that tick (never
// fatal).
func (r *Reporter) countMCPExternalAuthConfigs(ctx context.Context) (uint32, error) {
	list := &mcpv1beta1.MCPExternalAuthConfigList{}
	opts := []client.ListOption{}
	if r.namespace != "" {
		opts = append(opts, client.InNamespace(r.namespace))
	}
	if err := r.client.List(ctx, list, opts...); err != nil {
		return 0, fmt.Errorf("list MCPExternalAuthConfigs: %w", err)
	}
	return clampCountUint32(len(list.Items)), nil
}

// clampCountUint32 converts a resource list length to uint32, clamping to the
// UInt32 column range. A negative length is impossible and an overflow is
// implausible for these resource counts; the clamp is purely defensive.
func clampCountUint32(n int) uint32 {
	if n < 0 || n > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(n)
}
