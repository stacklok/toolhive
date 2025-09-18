// Package api contains the REST API for ToolHive.
package api

// The OpenAPI spec is generated using "github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc4"
// To update the OpenAPI spec, run:
// install swag:
//	go install github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc4
// generate the spec:
//	swag init -g pkg/api/server.go --v3.1 -o docs/server

// @title           ToolHive API
// @version         1.0
// @description     This is the ToolHive API server.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	v1 "github.com/stacklok/toolhive/pkg/api/v1"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// Not sure if these values need to be configurable.
const (
	middlewareTimeout = 60 * time.Second
	readHeaderTimeout = 10 * time.Second
	socketPermissions = 0660 // Socket file permissions (owner/group read-write)
)

// ServerBuilder provides a fluent interface for building and configuring the API server
type ServerBuilder struct {
	address          string
	isUnixSocket     bool
	debugMode        bool
	enableDocs       bool
	oidcConfig       *auth.TokenValidatorConfig
	middlewares      []func(http.Handler) http.Handler
	customRoutes     map[string]http.Handler
	containerRuntime runtime.Runtime
	clientManager    client.Manager
	workloadManager  workloads.Manager
	groupManager     groups.Manager
}

// NewServerBuilder creates a new ServerBuilder with default configuration
func NewServerBuilder() *ServerBuilder {
	return &ServerBuilder{
		middlewares:  make([]func(http.Handler) http.Handler, 0),
		customRoutes: make(map[string]http.Handler),
	}
}

// WithAddress sets the server address
func (b *ServerBuilder) WithAddress(address string) *ServerBuilder {
	b.address = address
	return b
}

// WithUnixSocket configures the server to use a Unix socket
func (b *ServerBuilder) WithUnixSocket(isUnixSocket bool) *ServerBuilder {
	b.isUnixSocket = isUnixSocket
	return b
}

// WithDebugMode enables or disables debug mode
func (b *ServerBuilder) WithDebugMode(debugMode bool) *ServerBuilder {
	b.debugMode = debugMode
	return b
}

// WithDocs enables or disables OpenAPI documentation
func (b *ServerBuilder) WithDocs(enableDocs bool) *ServerBuilder {
	b.enableDocs = enableDocs
	return b
}

// WithOIDCConfig sets the OIDC configuration
func (b *ServerBuilder) WithOIDCConfig(oidcConfig *auth.TokenValidatorConfig) *ServerBuilder {
	b.oidcConfig = oidcConfig
	return b
}

// WithMiddleware adds middleware to the server
func (b *ServerBuilder) WithMiddleware(mw ...func(http.Handler) http.Handler) *ServerBuilder {
	b.middlewares = append(b.middlewares, mw...)
	return b
}

// WithRoute adds a custom route to the server
func (b *ServerBuilder) WithRoute(prefix string, handler http.Handler) *ServerBuilder {
	b.customRoutes[prefix] = handler
	return b
}

// WithContainerRuntime sets the container runtime
func (b *ServerBuilder) WithContainerRuntime(containerRuntime runtime.Runtime) *ServerBuilder {
	b.containerRuntime = containerRuntime
	return b
}

// WithClientManager sets the client manager
func (b *ServerBuilder) WithClientManager(manager client.Manager) *ServerBuilder {
	b.clientManager = manager
	return b
}

// WithWorkloadManager sets the workload manager
func (b *ServerBuilder) WithWorkloadManager(manager workloads.Manager) *ServerBuilder {
	b.workloadManager = manager
	return b
}

// WithGroupManager sets the group manager
func (b *ServerBuilder) WithGroupManager(manager groups.Manager) *ServerBuilder {
	b.groupManager = manager
	return b
}

// Build creates and configures the HTTP router
func (b *ServerBuilder) Build(ctx context.Context) (*chi.Mux, error) {
	r := chi.NewRouter()

	// Apply default middleware
	r.Use(
		middleware.RequestID,
		// TODO: Figure out logging middleware. We may want to use a different logger.
		middleware.Timeout(middlewareTimeout),
		headersMiddleware,
	)

	// Add update check middleware
	r.Use(updateCheckMiddleware())

	// Add authentication middleware
	authMiddleware, _, err := auth.GetAuthenticationMiddleware(ctx, b.oidcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create authentication middleware: %v", err)
	}
	r.Use(authMiddleware)

	// Apply custom middleware
	for _, mw := range b.middlewares {
		r.Use(mw)
	}

	// Create default managers if not provided
	if err := b.createDefaultManagers(ctx); err != nil {
		return nil, err
	}

	// Setup default routes
	b.setupDefaultRoutes(r)

	// Add custom routes
	for prefix, handler := range b.customRoutes {
		r.Mount(prefix, handler)
	}

	return r, nil
}

// createDefaultManagers creates default managers if they weren't provided
func (b *ServerBuilder) createDefaultManagers(ctx context.Context) error {
	var err error

	if b.containerRuntime == nil {
		b.containerRuntime, err = container.NewFactory().Create(ctx)
		if err != nil {
			return fmt.Errorf("failed to create container runtime: %v", err)
		}
	}

	if b.clientManager == nil {
		b.clientManager, err = client.NewManager(ctx)
		if err != nil {
			return fmt.Errorf("failed to create client manager: %v", err)
		}
	}

	if b.workloadManager == nil {
		b.workloadManager, err = workloads.NewManagerFromRuntime(b.containerRuntime)
		if err != nil {
			return fmt.Errorf("failed to create workload manager: %v", err)
		}
	}

	if b.groupManager == nil {
		b.groupManager, err = groups.NewManager()
		if err != nil {
			return fmt.Errorf("failed to create group manager: %v", err)
		}
	}

	return nil
}

// setupDefaultRoutes sets up the default API routes
func (b *ServerBuilder) setupDefaultRoutes(r *chi.Mux) {
	routers := map[string]http.Handler{
		"/health":             v1.HealthcheckRouter(b.containerRuntime),
		"/api/v1beta/version": v1.VersionRouter(),
		"/api/v1beta/workloads": v1.WorkloadRouter(
			b.workloadManager,
			b.containerRuntime,
			b.groupManager,
			b.debugMode,
		),
		"/api/v1beta/registry":  v1.RegistryRouter(),
		"/api/v1beta/discovery": v1.DiscoveryRouter(),
		"/api/v1beta/clients":   v1.ClientRouter(b.clientManager, b.workloadManager, b.groupManager),
		"/api/v1beta/secrets":   v1.SecretsRouter(),
		"/api/v1beta/groups":    v1.GroupsRouter(b.groupManager, b.workloadManager, b.clientManager),
	}

	// Only mount docs router if enabled
	if b.enableDocs {
		routers["/api/"] = DocsRouter()
	}

	for prefix, router := range routers {
		r.Mount(prefix, router)
	}
}

func setupTCPListener(address string) (net.Listener, error) {
	return net.Listen("tcp", address)
}

func setupUnixSocket(address string) (net.Listener, error) {
	// Remove the socket file if it already exists
	if _, err := os.Stat(address); err == nil {
		if err := os.Remove(address); err != nil {
			return nil, fmt.Errorf("failed to remove existing socket: %v", err)
		}
	}

	// Create the directory for the socket file if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(address), 0750); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %v", err)
	}

	// Create UNIX socket listener
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, fmt.Errorf("failed to create UNIX socket listener: %v", err)
	}

	// Set file permissions on the socket to allow other local processes to connect
	if err := os.Chmod(address, socketPermissions); err != nil {
		return nil, fmt.Errorf("failed to set socket permissions: %v", err)
	}

	return listener, nil
}

func cleanupUnixSocket(address string) {
	if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
		logger.Warnf("failed to remove socket file: %v", err)
	}
}

func headersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
		}
		next.ServeHTTP(w, r)
	})
}

// updateCheckMiddleware triggers update checks for API usage
func updateCheckMiddleware() func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			go func() {
				if updates.ShouldSkipUpdateChecks() {
					return
				}
				component, version, uiReleaseBuild := getComponentAndVersionFromRequest(r)
				versionClient := updates.NewVersionClientForComponent(component, version, uiReleaseBuild)

				updateChecker, err := updates.NewUpdateChecker(versionClient)
				if err != nil {
					logger.Warnf("unable to create update client for %s: %s", component, err)
					return
				}

				err = updateChecker.CheckLatestVersion()
				if err != nil {
					logger.Warnf("could not check for updates for %s: %s", component, err)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// getComponentAndVersionFromRequest determines the component name, version, and ui release build from the request
func getComponentAndVersionFromRequest(r *http.Request) (string, string, bool) {
	clientType := r.Header.Get("X-Client-Type")

	if clientType == "toolhive-studio" {
		version := r.Header.Get("X-Client-Version")
		// Checks if the UI is calling from an official release
		uiReleaseBuild := r.Header.Get("X-Client-Release-Build") == "true"
		return "UI", version, uiReleaseBuild
	}

	return "API", "", false
}

// Server represents a configured HTTP server
type Server struct {
	httpServer   *http.Server
	listener     net.Listener
	address      string
	isUnixSocket bool
	addrType     string
}

// NewServer creates a new Server instance from a pre-configured builder
func NewServer(ctx context.Context, builder *ServerBuilder) (*Server, error) {
	handler, err := builder.Build(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build server handler: %w", err)
	}

	listener, addrType, err := createListener(builder.address, builder.isUnixSocket)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}

	httpServer := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Addr:              builder.address,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	return &Server{
		httpServer:   httpServer,
		listener:     listener,
		address:      builder.address,
		isUnixSocket: builder.isUnixSocket,
		addrType:     addrType,
	}, nil
}

// Start starts the server and blocks until the context is cancelled
func (s *Server) Start(ctx context.Context) error {
	logger.Infof("starting %s server at %s", s.addrType, s.address)

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		if err := s.httpServer.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- fmt.Errorf("server stopped with error: %w", err)
		}
		close(serverErr)
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		return s.shutdown()
	case err := <-serverErr:
		if err != nil {
			s.cleanup()
			return err
		}
		return nil
	}
}

// shutdown gracefully shuts down the server
func (s *Server) shutdown() error {
	if err := s.httpServer.Shutdown(context.Background()); err != nil {
		s.cleanup()
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	s.cleanup()
	logger.Infof("%s server stopped", s.addrType)
	return nil
}

// cleanup performs cleanup operations
func (s *Server) cleanup() {
	if s.isUnixSocket {
		cleanupUnixSocket(s.address)
	}
}

// createListener creates the appropriate listener based on the configuration
func createListener(address string, isUnixSocket bool) (net.Listener, string, error) {
	var listener net.Listener
	var addrType string
	var err error

	if isUnixSocket {
		listener, err = setupUnixSocket(address)
		addrType = "UNIX socket"
	} else {
		listener, err = setupTCPListener(address)
		addrType = "HTTP"
	}

	if err != nil {
		return nil, "", err
	}

	return listener, addrType, nil
}

// Serve starts the server on the given address and serves the API.
// It is assumed that the caller sets up appropriate signal handling.
// If isUnixSocket is true, address is treated as a UNIX socket path.
// If oidcConfig is provided, OIDC authentication will be enabled for all API endpoints.
func Serve(
	ctx context.Context,
	address string,
	isUnixSocket bool,
	debugMode bool,
	enableDocs bool,
	oidcConfig *auth.TokenValidatorConfig,
	middlewares ...func(http.Handler) http.Handler,
) error {
	builder := NewServerBuilder().
		WithAddress(address).
		WithUnixSocket(isUnixSocket).
		WithDebugMode(debugMode).
		WithDocs(enableDocs).
		WithOIDCConfig(oidcConfig).
		WithMiddleware(middlewares...)

	server, err := NewServer(ctx, builder)
	if err != nil {
		return err
	}

	return server.Start(ctx)
}
