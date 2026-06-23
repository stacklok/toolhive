// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package app contains the entry-point and pre-Run setup for the ToolHive
// Kubernetes Operator. Run is the single exported entry point and absorbs
// all behavior that used to live in cmd/thv-operator/main.go's func main().
package app

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server" // Import for metricsserver
	"sigs.k8s.io/controller-runtime/pkg/webhook"                      // Import for webhook

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/imagepullsecrets"
	// Import authorizer backends so they register with the factory registry.
	// Placed in the binary entrypoint (not the controller) to keep the
	// MCPAuthzConfig controller backend-agnostic.
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/cedar"
	_ "github.com/stacklok/toolhive/pkg/authz/authorizers/http"
	"github.com/stacklok/toolhive/pkg/operator/telemetry"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = log.Log.WithName("setup")
)

// envEnableStorageVersionMigrator gates the StorageVersionMigrator controller.
// The binary itself defaults to OFF when the var is unset; the operator helm
// chart sets it to "true" by default, so chart-based installs run the migrator
// unless the operator explicitly opts out. Set to "true" (or "1", "t") to
// enable, "false" to disable.
const envEnableStorageVersionMigrator = "TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR"

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))
	utilruntime.Must(mcpv1beta1.AddToScheme(scheme))
	utilruntime.Must(apiextensionsv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

// Run is the operator entry point. It blocks until the signal context is cancelled; on fatal setup errors it calls os.Exit(1).
func Run() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	// Initialize the controller-runtime logger. Without this call, controller-runtime
	// uses a no-op logger by default and ALL operator log output is silently discarded.
	// Bridge to slog for consistency with the rest of the ToolHive codebase.
	ctrl.SetLogger(logr.FromSlogHandler(slog.Default().Handler()))

	podNamespace, _ := os.LookupEnv("POD_NAMESPACE")

	options := ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddr},
		WebhookServer:           webhook.NewServer(webhook.Options{Port: 9443}),
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "toolhive-operator-leader-election",
		LeaderElectionNamespace: podNamespace,
		Cache: cache.Options{
			// if nil, defaults to all namespaces
			DefaultNamespaces: getDefaultNamespaces(),
		},
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), options)
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Parse cluster-wide default imagePullSecrets once at startup. The Defaults
	// value is shared (by copy) with every reconciler that constructs workloads.
	imagePullSecretsDefaults := imagepullsecrets.LoadDefaultsFromEnv()
	if defaults := imagePullSecretsDefaults.List(); len(defaults) > 0 {
		names := make([]string, 0, len(defaults))
		for _, ref := range defaults {
			names = append(names, ref.Name)
		}
		setupLog.Info("loaded cluster-wide default imagePullSecrets", "imagePullSecrets", names)
	} else if rawValue, set := os.LookupEnv(imagepullsecrets.EnvVar); set && rawValue != "" {
		// The env var was set but parsed to nothing — likely a typo such as
		// " , " or ",,,". Surface this so the misconfiguration is diagnosable
		// instead of being silently ignored.
		setupLog.Info(
			"TOOLHIVE_DEFAULT_IMAGE_PULL_SECRETS is set but contains no valid secret names; "+
				"chart-level defaults will not be applied",
			"imagePullSecrets", rawValue,
		)
	}

	if err := setupControllersAndWebhooks(mgr, imagePullSecretsDefaults); err != nil {
		setupLog.Error(err, "unable to setup controllers and webhooks")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	// Set up telemetry service - only runs when elected as leader
	telemetryService := telemetry.NewService(mgr.GetClient(), podNamespace)
	if err := mgr.Add(&telemetry.LeaderTelemetryRunnable{
		TelemetryService: telemetryService,
	}); err != nil {
		setupLog.Error(err, "unable to add telemetry runnable")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}

// setupControllersAndWebhooks sets up all controllers and webhooks with the manager.
// The imagePullSecretsDefaults are propagated to controllers that construct
// workloads so that chart-level defaults are applied alongside per-CR overrides.
func setupControllersAndWebhooks(mgr ctrl.Manager, imagePullSecretsDefaults imagepullsecrets.Defaults) error {
	if err := setupServerControllers(mgr, imagePullSecretsDefaults); err != nil {
		return err
	}
	if err := setupRegistryController(mgr, imagePullSecretsDefaults); err != nil {
		return err
	}
	if err := setupAggregationControllers(mgr, imagePullSecretsDefaults); err != nil {
		return err
	}
	enabled, err := isStorageVersionMigratorEnabled()
	if err != nil {
		return err
	}
	if enabled {
		if err := setupStorageVersionMigrator(mgr); err != nil {
			return err
		}
	} else {
		setupLog.V(1).Info("StorageVersionMigrator disabled", "envVar", envEnableStorageVersionMigrator)
	}
	//+kubebuilder:scaffold:builder
	return nil
}

// setupStorageVersionMigrator wires the StorageVersionMigrator controller into
// the manager. The controller reconciles status.storedVersions on opted-in
// toolhive.stacklok.dev CRDs so a future operator release can drop deprecated
// versions from spec.versions without orphaning etcd objects.
func setupStorageVersionMigrator(mgr ctrl.Manager) error {
	if err := (&controllers.StorageVersionMigratorReconciler{
		Client:    mgr.GetClient(),
		APIReader: mgr.GetAPIReader(),
		Scheme:    mgr.GetScheme(),
		Recorder:  mgr.GetEventRecorder("storageversionmigrator-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller StorageVersionMigrator: %w", err)
	}
	return nil
}

// isStorageVersionMigratorEnabled reports whether the StorageVersionMigrator
// controller should be registered. Defaults to false when
// TOOLHIVE_ENABLE_STORAGE_VERSION_MIGRATOR is unset; the operator helm chart
// sets it to "true" by default. An unparsable value returns an error so startup
// fails loudly rather than silently disabling the feature an admin asked to turn on.
func isStorageVersionMigratorEnabled() (bool, error) {
	value, found := os.LookupEnv(envEnableStorageVersionMigrator)
	if !found {
		return false, nil
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf(
			"invalid value for %s: %q (expected true/false): %w",
			envEnableStorageVersionMigrator, value, err)
	}
	return enabled, nil
}

// setupGroupRefFieldIndexes sets up field indexing for spec.groupRef on all resource types
// that can reference an MCPGroup. This enables efficient lookups by groupRef in controllers.
func setupGroupRefFieldIndexes(mgr ctrl.Manager) error {
	// MCPServer.Spec.GroupRef
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1beta1.MCPServer{},
		"spec.groupRef",
		func(obj client.Object) []string {
			mcpServer := obj.(*mcpv1beta1.MCPServer)
			name := mcpServer.Spec.GroupRef.GetName()
			if name == "" {
				return nil
			}
			return []string{name}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPServer spec.groupRef: %w", err)
	}

	// MCPRemoteProxy.Spec.GroupRef
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1beta1.MCPRemoteProxy{},
		"spec.groupRef",
		func(obj client.Object) []string {
			mcpRemoteProxy := obj.(*mcpv1beta1.MCPRemoteProxy)
			name := mcpRemoteProxy.Spec.GroupRef.GetName()
			if name == "" {
				return nil
			}
			return []string{name}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPRemoteProxy spec.groupRef: %w", err)
	}

	// MCPServerEntry.Spec.GroupRef
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1beta1.MCPServerEntry{},
		"spec.groupRef",
		func(obj client.Object) []string {
			mcpServerEntry := obj.(*mcpv1beta1.MCPServerEntry)
			name := mcpServerEntry.Spec.GroupRef.GetName()
			if name == "" {
				return nil
			}
			return []string{name}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPServerEntry spec.groupRef: %w", err)
	}

	return nil
}

// setupServerControllers sets up server-related controllers
// (MCPServer, MCPExternalAuthConfig, MCPRemoteProxy, MCPServerEntry, ToolConfig).
// imagePullSecretsDefaults are merged with per-CR imagePullSecrets when
// reconcilers construct workloads.
func setupServerControllers(mgr ctrl.Manager, imagePullSecretsDefaults imagepullsecrets.Defaults) error {
	if err := setupGroupRefFieldIndexes(mgr); err != nil {
		return err
	}

	// Set up MCPServer controller
	rec := &controllers.MCPServerReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Recorder:                 mgr.GetEventRecorder("mcpserver-controller"),
		PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
		ImagePullSecretsDefaults: imagePullSecretsDefaults,
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPServer: %w", err)
	}

	// Set up MCPToolConfig controller
	if err := (&controllers.ToolConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPToolConfig: %w", err)
	}

	// Set up MCPExternalAuthConfig controller
	if err := (&controllers.MCPExternalAuthConfigReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("mcpexternalauthconfig-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPExternalAuthConfig: %w", err)
	}

	// Set up MCPOIDCConfig controller
	if err := (&controllers.MCPOIDCConfigReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("mcpoidcconfig-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPOIDCConfig: %w", err)
	}

	// Set up MCPAuthzConfig controller
	if err := (&controllers.MCPAuthzConfigReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("mcpauthzconfig-controller"),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPAuthzConfig: %w", err)
	}

	// Set up MCPTelemetryConfig controller
	if err := (&controllers.MCPTelemetryConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPTelemetryConfig: %w", err)
	}

	// Set up MCPWebhookConfig controller
	if err := (&controllers.MCPWebhookConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPWebhookConfig: %w", err)
	}

	// Set up MCPRemoteProxy controller
	if err := (&controllers.MCPRemoteProxyReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Recorder:                 mgr.GetEventRecorder("mcpremoteproxy-controller"),
		PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
		ImagePullSecretsDefaults: imagePullSecretsDefaults,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPRemoteProxy: %w", err)
	}

	// Set up EmbeddingServer controller
	if err := (&controllers.EmbeddingServerReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Recorder:                 mgr.GetEventRecorder("embeddingserver-controller"),
		PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
		ImagePullSecretsDefaults: imagePullSecretsDefaults,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller EmbeddingServer: %w", err)
	}

	// Set up MCPServerEntry controller (validation-only, no infrastructure)
	if err := (&controllers.MCPServerEntryReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPServerEntry: %w", err)
	}

	return nil
}

// setupRegistryController sets up the MCPRegistry controller.
// imagePullSecretsDefaults are merged with mcpRegistry.Spec.ImagePullSecrets
// when the registry-api workload is constructed.
func setupRegistryController(mgr ctrl.Manager, imagePullSecretsDefaults imagepullsecrets.Defaults) error {
	rec := controllers.NewMCPRegistryReconciler(
		mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorder("mcpregistry-controller"), imagePullSecretsDefaults)
	if err := rec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPRegistry: %w", err)
	}
	return nil
}

// setupAggregationControllers sets up Virtual MCP-related controllers and webhooks
// (MCPGroup, VirtualMCPServer, and their webhooks). Must run after
// setupServerControllers, which creates the MCPServer.Spec.GroupRef field index
// these controllers depend on.
// imagePullSecretsDefaults are merged with vmcp.Spec.ImagePullSecrets when the
// VirtualMCPServer Deployment is constructed.
func setupAggregationControllers(mgr ctrl.Manager, imagePullSecretsDefaults imagepullsecrets.Defaults) error {
	// Set up MCPGroup controller
	if err := (&controllers.MCPGroupReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPGroup: %w", err)
	}

	// Set up VirtualMCPServer controller
	if err := (&controllers.VirtualMCPServerReconciler{
		Client:                   mgr.GetClient(),
		Scheme:                   mgr.GetScheme(),
		Recorder:                 mgr.GetEventRecorder("virtualmcpserver-controller"),
		PlatformDetector:         ctrlutil.NewSharedPlatformDetector(),
		ImagePullSecretsDefaults: imagePullSecretsDefaults,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller VirtualMCPServer: %w", err)
	}

	return nil
}

// getDefaultNamespaces returns a map of namespaces to cache.Config for the operator to watch.
// if WATCH_NAMESPACE is not set, returns nil which is defaulted to a cluster scope.
func getDefaultNamespaces() map[string]cache.Config {

	// WATCH_NAMESPACE specifies the namespace(s) to watch.
	// An empty value means the operator is running with cluster scope.
	watchNamespace, found := os.LookupEnv("WATCH_NAMESPACE")
	if !found {
		return nil
	}

	namespaces := make(map[string]cache.Config)
	if watchNamespace != "" {
		for _, ns := range strings.Split(watchNamespace, ",") {
			namespaces[ns] = cache.Config{}
		}
	}
	return namespaces
}
