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
The server reads registry data from ConfigMaps and provides REST endpoints for clients.`,
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
	serveCmd.Flags().String("configmap", "", "ConfigMap name containing registry data")

	err := viper.BindPFlag("address", serveCmd.Flags().Lookup("address"))
	if err != nil {
		logger.Fatalf("Failed to bind address flag: %v", err)
	}
	err = viper.BindPFlag("configmap", serveCmd.Flags().Lookup("configmap"))
	if err != nil {
		logger.Fatalf("Failed to bind configmap flag: %v", err)
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

func runServe(_ *cobra.Command, _ []string) error {
	ctx := context.Background()

	// Get configuration
	address := viper.GetString("address")
	configMapName := viper.GetString("configmap")

	if configMapName == "" {
		return fmt.Errorf("configmap flag is required")
	}

	namespace := thvk8scli.GetCurrentNamespace()

	logger.Infof("Starting registry API server on %s", address)
	logger.Infof("ConfigMap: %s, Namespace: %s", configMapName, namespace)

	// Create Kubernetes client and providers
	var registryProvider service.RegistryDataProvider
	var deploymentProvider service.DeploymentProvider

	// Get Kubernetes config
	config, err := getKubernetesConfig()
	if err != nil {
		return fmt.Errorf("failed to create kubernetes config: %w", err)
	}

	// Create Kubernetes client
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	// Create the Kubernetes-based registry data provider
	registryProvider = service.NewK8sRegistryDataProvider(clientset, configMapName, namespace)
	logger.Infof("Created Kubernetes registry data provider for ConfigMap %s/%s", namespace, configMapName)

	// Create the Kubernetes-based deployment provider
	deploymentProvider, err = service.NewK8sDeploymentProvider(config, configMapName)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes deployment provider: %w", err)
	}
	logger.Infof("Created Kubernetes deployment provider for registry: %s", configMapName)

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
