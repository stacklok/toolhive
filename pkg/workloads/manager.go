// Package workloads contains high-level logic for managing the lifecycle of
// ToolHive-managed containers.
package workloads

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container/kubernetes"
	rt "github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/core"
	"github.com/stacklok/toolhive/pkg/runner"
)

// Manager is responsible for managing the state of ToolHive-managed containers.
// NOTE: This interface may be split up in future PRs, in particular, operations
// which are only relevant to the CLI/API use case will be split out.
//
//go:generate mockgen -destination=mocks/mock_manager.go -package=mocks -source=manager.go Manager
type Manager interface {
	// GetWorkload retrieves details of the named workload including its status.
	GetWorkload(ctx context.Context, workloadName string) (core.Workload, error)
	// ListWorkloads retrieves the states of all workloads.
	// The `listAll` parameter determines whether to include workloads that are not running.
	// The optional `labelFilters` parameter allows filtering workloads by labels (format: key=value).
	ListWorkloads(ctx context.Context, listAll bool, labelFilters ...string) ([]core.Workload, error)
	// DeleteWorkloads deletes the specified workloads by name.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	DeleteWorkloads(ctx context.Context, names []string) (*errgroup.Group, error)
	// StopWorkloads stops the specified workloads by name.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	StopWorkloads(ctx context.Context, names []string) (*errgroup.Group, error)
	// RunWorkload runs a container in the foreground.
	RunWorkload(ctx context.Context, runConfig *runner.RunConfig) error
	// RunWorkloadDetached runs a container in the background.
	RunWorkloadDetached(ctx context.Context, runConfig *runner.RunConfig) error
	// RestartWorkloads restarts the specified workloads by name.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	RestartWorkloads(ctx context.Context, names []string, foreground bool) (*errgroup.Group, error)
	// UpdateWorkload updates a workload by stopping, deleting, and recreating it.
	// It is implemented as an asynchronous operation which returns an errgroup.Group
	UpdateWorkload(ctx context.Context, workloadName string, newConfig *runner.RunConfig) (*errgroup.Group, error)
	// GetLogs retrieves the logs of a container.
	GetLogs(ctx context.Context, containerName string, follow bool) (string, error)
	// GetProxyLogs retrieves the proxy logs from the filesystem.
	GetProxyLogs(ctx context.Context, workloadName string) (string, error)
	// MoveToGroup moves the specified workloads from one group to another by updating their runconfig.
	MoveToGroup(ctx context.Context, workloadNames []string, groupFrom string, groupTo string) error
	// ListWorkloadsInGroup returns all workload names that belong to the specified group, including stopped workloads.
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]string, error)
	// DoesWorkloadExist checks if a workload with the given name exists.
	DoesWorkloadExist(ctx context.Context, workloadName string) (bool, error)
}

// ErrWorkloadNotRunning is returned when a container cannot be found by name.
var ErrWorkloadNotRunning = fmt.Errorf("workload not running")

// NewManager creates a new CLI workload manager.
// Returns Manager interface (existing behavior, unchanged).
// IMPORTANT: This function only works in CLI mode. For Kubernetes, use NewK8SManager() directly.
func NewManager(ctx context.Context) (Manager, error) {
	if rt.IsKubernetesRuntime() {
		return nil, fmt.Errorf("use workloads.NewK8SManager() for Kubernetes environments")
	}
	return NewCLIManager(ctx)
}

// NewManagerWithProvider creates a new CLI workload manager with a custom config provider.
// IMPORTANT: This function only works in CLI mode. For Kubernetes, use NewK8SManager() directly.
func NewManagerWithProvider(ctx context.Context, configProvider config.Provider) (Manager, error) {
	if rt.IsKubernetesRuntime() {
		return nil, fmt.Errorf("use workloads.NewK8SManager() for Kubernetes environments")
	}
	return NewCLIManagerWithProvider(ctx, configProvider)
}

// NewManagerFromRuntime creates a new CLI workload manager from an existing runtime.
// IMPORTANT: This function only works in CLI mode. For Kubernetes, use NewK8SManager() directly.
// This function checks the runtime type directly, not the environment, to support
// cases like proxyrunner which runs in Kubernetes pods but uses Docker runtime.
func NewManagerFromRuntime(rtRuntime rt.Runtime) (Manager, error) {
	// Check if the runtime is actually a Kubernetes runtime by type assertion
	// The proxyrunner runs in pods but uses Docker runtime, so we check the runtime type,
	// not the environment (which would always be Kubernetes in pods)
	if _, ok := rtRuntime.(*kubernetes.Client); ok {
		return nil, fmt.Errorf("use workloads.NewK8SManager() for Kubernetes runtime")
	}

	return NewCLIManagerFromRuntime(rtRuntime)
}

// NewManagerFromRuntimeWithProvider creates a new CLI workload manager from an existing runtime with a custom config provider.
// IMPORTANT: This function only works in CLI mode. For Kubernetes, use NewK8SManager() directly.
// This function checks the runtime type directly, not the environment, to support
// cases like proxyrunner which runs in Kubernetes pods but uses Docker runtime.
func NewManagerFromRuntimeWithProvider(rtRuntime rt.Runtime, configProvider config.Provider) (Manager, error) {
	// Check if the runtime is actually a Kubernetes runtime by type assertion
	// The proxyrunner runs in pods but uses Docker runtime, so we check the runtime type,
	// not the environment (which would always be Kubernetes in pods)
	if _, ok := rtRuntime.(*kubernetes.Client); ok {
		return nil, fmt.Errorf("use workloads.NewK8SManager() for Kubernetes runtime")
	}
	return NewCLIManagerFromRuntimeWithProvider(rtRuntime, configProvider)
}

// NewK8SManagerFromContext creates a Kubernetes-based workload manager from context.
// It automatically sets up the Kubernetes client and detects the namespace.
func NewK8SManagerFromContext(_ context.Context) (K8SManager, error) {
	// Create a scheme for controller-runtime client
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))

	// Get Kubernetes config
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes config: %w", err)
	}

	// Create controller-runtime client
	k8sClient, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Detect namespace
	namespace := kubernetes.GetCurrentNamespace()

	return NewK8SManager(k8sClient, namespace)
}
