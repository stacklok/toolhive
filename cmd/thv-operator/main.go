// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package main is the entry point for the ToolHive Kubernetes Operator.
// It sets up and runs the controller manager for the MCPServer custom resource.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server" // Import for metricsserver
	"sigs.k8s.io/controller-runtime/pkg/webhook"                      // Import for webhook

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/cmd/thv-operator/controllers"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/operator/telemetry"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = log.Log.WithName("setup")
)

// Feature flags for controller groups
const (
	featureServer   = "ENABLE_SERVER"
	featureRegistry = "ENABLE_REGISTRY"
	featureVMCP     = "ENABLE_VMCP"
)

// controllerDependencies maps each controller group to its required dependencies
var controllerDependencies = map[string][]string{
	featureVMCP: {featureServer}, // Virtual MCP requires server controllers
}

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(mcpv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.Parse()

	// Initialize the structured logger
	logger.Initialize()

	// Set the controller-runtime logger to use our structured logger
	ctrl.SetLogger(logger.NewLogr())

	options := ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		WebhookServer:          webhook.NewServer(webhook.Options{Port: 9443}),
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "toolhive-operator-leader-election",
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

	if err := setupControllersAndWebhooks(mgr); err != nil {
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

	podNamespace, _ := os.LookupEnv("POD_NAMESPACE")
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

// setupControllersAndWebhooks sets up all controllers and webhooks with the manager
func setupControllersAndWebhooks(mgr ctrl.Manager) error {
	// Check feature flags
	enableServer := isFeatureEnabled(featureServer, true)
	enableRegistry := isFeatureEnabled(featureRegistry, true)
	enableVMCP := isFeatureEnabled(featureVMCP, true)

	// Track enabled features for dependency checking
	enabledFeatures := map[string]bool{
		featureServer:   enableServer,
		featureRegistry: enableRegistry,
		featureVMCP:     enableVMCP,
	}

	// Check dependencies and log warnings for missing dependencies
	for feature, deps := range controllerDependencies {
		if !enabledFeatures[feature] {
			continue // Skip if feature itself is disabled
		}
		for _, dep := range deps {
			if !enabledFeatures[dep] {
				setupLog.Info(
					fmt.Sprintf("%s requires %s to be enabled, skipping %s controllers", feature, dep, feature),
					"feature", feature,
					"required_dependency", dep,
				)
				enabledFeatures[feature] = false // Mark as effectively disabled
				break
			}
		}
	}

	// Set up server-related controllers
	if enabledFeatures[featureServer] {
		if err := setupServerControllers(mgr, enableRegistry); err != nil {
			return err
		}
	} else {
		setupLog.Info("ENABLE_SERVER is disabled, skipping server-related controllers")
	}

	// Set up registry controller
	if enabledFeatures[featureRegistry] {
		if err := setupRegistryController(mgr); err != nil {
			return err
		}
	} else {
		setupLog.Info("ENABLE_REGISTRY is disabled, skipping MCPRegistry controller")
	}

	// Set up Virtual MCP controllers and webhooks
	if enabledFeatures[featureVMCP] {
		if err := setupAggregationControllers(mgr); err != nil {
			return err
		}
	} else {
		setupLog.Info("ENABLE_VMCP is disabled, skipping Virtual MCP controllers and webhooks")
	}

	//+kubebuilder:scaffold:builder
	return nil
}

// setupServerControllers sets up server-related controllers (MCPServer, MCPExternalAuthConfig, MCPRemoteProxy, ToolConfig)
func setupServerControllers(mgr ctrl.Manager, enableRegistry bool) error {
	// Set up field indexing for MCPServer.Spec.GroupRef
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1alpha1.MCPServer{},
		"spec.groupRef",
		func(obj client.Object) []string {
			mcpServer := obj.(*mcpv1alpha1.MCPServer)
			if mcpServer.Spec.GroupRef == "" {
				return nil
			}
			return []string{mcpServer.Spec.GroupRef}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPServer spec.groupRef: %w", err)
	}

	// Set up field indexing for MCPRemoteProxy.Spec.GroupRef
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&mcpv1alpha1.MCPRemoteProxy{},
		"spec.groupRef",
		func(obj client.Object) []string {
			mcpRemoteProxy := obj.(*mcpv1alpha1.MCPRemoteProxy)
			if mcpRemoteProxy.Spec.GroupRef == "" {
				return nil
			}
			return []string{mcpRemoteProxy.Spec.GroupRef}
		},
	); err != nil {
		return fmt.Errorf("unable to create field index for MCPRemoteProxy spec.groupRef: %w", err)
	}

	// Set image validation mode based on whether registry is enabled
	// If ENABLE_REGISTRY is enabled, enforce registry-based image validation
	// Otherwise, allow all images
	imageValidation := validation.ImageValidationAlwaysAllow
	if enableRegistry {
		imageValidation = validation.ImageValidationRegistryEnforcing
	}

	// Set up MCPServer controller
	rec := &controllers.MCPServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("mcpserver-controller"),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
		ImageValidation:  imageValidation,
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
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPExternalAuthConfig: %w", err)
	}

	// Set up MCPRemoteProxy controller
	if err := (&controllers.MCPRemoteProxyReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPRemoteProxy: %w", err)
	}

	// Set up EmbeddingServer controller
	if err := (&controllers.EmbeddingServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("embeddingserver-controller"),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
		ImageValidation:  imageValidation,
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller EmbeddingServer: %w", err)
	}

	return nil
}

// setupRegistryController sets up the MCPRegistry controller
func setupRegistryController(mgr ctrl.Manager) error {
	if err := (controllers.NewMCPRegistryReconciler(mgr.GetClient(), mgr.GetScheme())).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPRegistry: %w", err)
	}
	return nil
}

// setupAggregationControllers sets up Virtual MCP-related controllers and webhooks
// (MCPGroup, VirtualMCPServer, and their webhooks)
// Note: This function assumes server controllers are enabled (enforced by dependency check)
// The field index for MCPServer.Spec.GroupRef is created in setupServerControllers
func setupAggregationControllers(mgr ctrl.Manager) error {
	// Set up MCPGroup controller
	if err := (&controllers.MCPGroupReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPGroup: %w", err)
	}

	// Set up VirtualMCPServer controller
	if err := (&controllers.VirtualMCPServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("virtualmcpserver-controller"),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller VirtualMCPServer: %w", err)
	}

	return nil
}

// isFeatureEnabled checks if a feature flag environment variable is enabled.
// If the environment variable is not set, it returns the default value.
// The environment variable is considered enabled if it's set to "true", "1", or "t" (case-insensitive).
// Invalid values (e.g., "yes", "enabled") will log a warning and return the default value.
func isFeatureEnabled(envVar string, defaultValue bool) bool {
	value, found := os.LookupEnv(envVar)
	if !found {
		return defaultValue
	}
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		setupLog.Info(
			"Invalid boolean value for feature flag, using default",
			"envVar", envVar,
			"value", value,
			"default", defaultValue,
			"validValues", "true, false, 1, 0, t, f",
		)
		return defaultValue
	}
	return enabled
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
