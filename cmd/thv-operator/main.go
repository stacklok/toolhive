// Package main is the entry point for the ToolHive Kubernetes Operator.
// It sets up and runs the controller manager for the MCPServer custom resource.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

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
		return fmt.Errorf("unable to create field index for spec.groupRef: %w", err)
	}

	// Create a shared platform detector for all controllers
	sharedPlatformDetector := ctrlutil.NewSharedPlatformDetector()
	rec := &controllers.MCPServerReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		Recorder:         mgr.GetEventRecorderFor("mcpserver-controller"),
		PlatformDetector: sharedPlatformDetector,
		ImageValidation:  validation.ImageValidationAlwaysAllow,
	}

	if err := rec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPServer: %w", err)
	}

	// Register MCPToolConfig controller
	if err := (&controllers.ToolConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPToolConfig: %w", err)
	}

	// Register MCPExternalAuthConfig controller
	if err := (&controllers.MCPExternalAuthConfigReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPExternalAuthConfig: %w", err)
	}

	// Register MCPRemoteProxy controller
	if err := (&controllers.MCPRemoteProxyReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		PlatformDetector: ctrlutil.NewSharedPlatformDetector(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPRemoteProxy: %w", err)
	}

	// Only register MCPRegistry controller if feature flag is enabled
	rec.ImageValidation = validation.ImageValidationRegistryEnforcing

	if err := (controllers.NewMCPRegistryReconciler(mgr.GetClient(), mgr.GetScheme())).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPRegistry: %w", err)
	}

	// Set up MCPGroup controller
	if err := (&controllers.MCPGroupReconciler{
		Client: mgr.GetClient(),
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create controller MCPGroup: %w", err)
	}

	// Set up VirtualMCPServer webhook
	if err := (&mcpv1alpha1.VirtualMCPServer{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create webhook VirtualMCPServer: %w", err)
	}

	// Set up VirtualMCPCompositeToolDefinition webhook
	if err := (&mcpv1alpha1.VirtualMCPCompositeToolDefinition{}).SetupWebhookWithManager(mgr); err != nil {
		return fmt.Errorf("unable to create webhook VirtualMCPCompositeToolDefinition: %w", err)
	}
	//+kubebuilder:scaffold:builder

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
