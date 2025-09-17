package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	v1 "github.com/stacklok/toolhive/cmd/thv-registry-api/api/v1"
	"github.com/stacklok/toolhive/cmd/thv-registry-api/internal/service"
	thvk8scli "github.com/stacklok/toolhive/pkg/container/kubernetes"
	"github.com/stacklok/toolhive/pkg/logger"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the registry API server",
	Long: `Start the registry API server to serve MCP registry data.
The server can read registry data from either:
- ConfigMaps using --configmap flag (requires Kubernetes API access)
- Local files using --file flag (for mounted ConfigMaps)

Both options require --registry-name to specify the registry identifier.
One of --configmap or --file must be specified.`,
	RunE: runServe,
}

const (
	defaultGracefulTimeout = 30 * time.Second // Kubernetes-friendly shutdown time
	serverRequestTimeout   = 10 * time.Second // Registry API should respond quickly
	serverReadTimeout      = 10 * time.Second // Enough for headers and small requests
	serverWriteTimeout     = 15 * time.Second // Must be > serverRequestTimeout to let middleware handle timeout
	serverIdleTimeout      = 60 * time.Second // Keep connections alive for reuse
)

func init() {
	serveCmd.Flags().String("address", ":8080", "Address to listen on")
	serveCmd.Flags().String("configmap", "", "ConfigMap name containing registry data (mutually exclusive with --file)")
	serveCmd.Flags().String("file", "", "File path to registry.json (mutually exclusive with --configmap)")
	serveCmd.Flags().String("registry-name", "", "Registry name identifier (required)")

	err := viper.BindPFlag("address", serveCmd.Flags().Lookup("address"))
	if err != nil {
		logger.Fatalf("Failed to bind address flag: %v", err)
	}
	err = viper.BindPFlag("configmap", serveCmd.Flags().Lookup("configmap"))
	if err != nil {
		logger.Fatalf("Failed to bind configmap flag: %v", err)
	}
	err = viper.BindPFlag("file", serveCmd.Flags().Lookup("file"))
	if err != nil {
		logger.Fatalf("Failed to bind file flag: %v", err)
	}
	err = viper.BindPFlag("registry-name", serveCmd.Flags().Lookup("registry-name"))
	if err != nil {
		logger.Fatalf("Failed to bind registry-name flag: %v", err)
	}
}

// getKubernetesConfig returns a Kubernetes REST config
func getKubernetesConfig() (*rest.Config, error) {
	// Try in-cluster config first
	config, err := rest.InClusterConfig()
	if err == nil {
		return config, nil
	}

	// Fall back to kubeconfig
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)
	return kubeConfig.ClientConfig()
}

// buildProviderConfig creates provider configuration based on command-line flags
func buildProviderConfig() (*service.RegistryProviderConfig, error) {
	configMapName := viper.GetString("configmap")
	filePath := viper.GetString("file")
	registryName := viper.GetString("registry-name")

	// Validate mutual exclusivity
	if configMapName != "" && filePath != "" {
		return nil, fmt.Errorf("--configmap and --file flags are mutually exclusive")
	}

	// Require one of the flags
	if configMapName == "" && filePath == "" {
		return nil, fmt.Errorf("either --configmap or --file flag is required")
	}

	// Require registry name
	if registryName == "" {
		return nil, fmt.Errorf("--registry-name flag is required")
	}

	if configMapName != "" {
		config, err := getKubernetesConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes config: %w", err)
		}

		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
		}

		return &service.RegistryProviderConfig{
			Type: service.RegistryProviderTypeConfigMap,
			ConfigMap: &service.ConfigMapProviderConfig{
				Name:         configMapName,
				Namespace:    thvk8scli.GetCurrentNamespace(),
				Clientset:    clientset,
				RegistryName: registryName,
			},
		}, nil
	}

	return &service.RegistryProviderConfig{
		Type: service.RegistryProviderTypeFile,
		File: &service.FileProviderConfig{
			FilePath:     filePath,
			RegistryName: registryName,
		},
	}, nil
}

func runServe(_ *cobra.Command, _ []string) error {
	ctx := context.Background()
	address := viper.GetString("address")

	logger.Infof("Starting registry API server on %s", address)

	providerConfig, err := buildProviderConfig()
	if err != nil {
		return fmt.Errorf("failed to build provider configuration: %w", err)
	}

	if err := providerConfig.Validate(); err != nil {
		return fmt.Errorf("invalid provider configuration: %w", err)
	}

	factory := service.NewRegistryProviderFactory()
	registryProvider, err := factory.CreateProvider(providerConfig)
	if err != nil {
		return fmt.Errorf("failed to create registry provider: %w", err)
	}

	logger.Infof("Created registry data provider: %s", registryProvider.GetSource())

	var deploymentProvider service.DeploymentProvider
	config, err := getKubernetesConfig()
	if err != nil {
		return fmt.Errorf("failed to create kubernetes config for deployment provider: %w", err)
	}

	// Use registry name from provider
	registryName := registryProvider.GetRegistryName()

	deploymentProvider, err = service.NewK8sDeploymentProvider(config, registryName)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes deployment provider: %w", err)
	}
	logger.Infof("Created Kubernetes deployment provider for registry: %s", registryName)

	// Create the registry service
	svc, err := service.NewService(ctx, registryProvider, deploymentProvider)
	if err != nil {
		return fmt.Errorf("failed to create registry service: %w", err)
	}

	// Create the registry server with middleware
	router := v1.NewServer(svc,
		v1.WithMiddlewares(
			middleware.RequestID,
			middleware.RealIP,
			middleware.Recoverer,
			middleware.Timeout(serverRequestTimeout),
			v1.LoggingMiddleware,
		),
	)

	// Create HTTP server
	server := &http.Server{
		Addr:         address,
		Handler:      router,
		ReadTimeout:  serverReadTimeout,
		WriteTimeout: serverWriteTimeout,
		IdleTimeout:  serverIdleTimeout,
	}

	// Start server in goroutine
	go func() {
		logger.Infof("Server listening on %s", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("Shutting down server...")

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), defaultGracefulTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
		return err
	}

	logger.Info("Server shutdown complete")
	return nil
}
