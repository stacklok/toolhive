// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	ociskills "github.com/stacklok/toolhive-core/oci/skills"
	regtypes "github.com/stacklok/toolhive-core/registry/types"
	v1 "github.com/stacklok/toolhive/pkg/api/v1"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/client"
	"github.com/stacklok/toolhive/pkg/config"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/groups"
	"github.com/stacklok/toolhive/pkg/recovery"
	"github.com/stacklok/toolhive/pkg/registry"
	"github.com/stacklok/toolhive/pkg/server/discovery"
	"github.com/stacklok/toolhive/pkg/skills"
	"github.com/stacklok/toolhive/pkg/skills/gitresolver"
	"github.com/stacklok/toolhive/pkg/skills/skillsvc"
	"github.com/stacklok/toolhive/pkg/storage/sqlite"
	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/workloads"
)

// Not sure if these values need to be configurable.
const (
	middlewareTimeout  = 60 * time.Second
	readHeaderTimeout  = 10 * time.Second
	shutdownTimeout    = 30 * time.Second
	nonceBytes         = 16
	socketPermissions  = 0660    // Socket file permissions (owner/group read-write)
	maxRequestBodySize = 1 << 20 // 1MB - Maximum request body size
)

// ServerBuilder provides a fluent interface for building and configuring the API server
type ServerBuilder struct {
	address          string
	isUnixSocket     bool
	debugMode        bool
	enableDocs       bool
	nonce            string
	oidcConfig       *auth.TokenValidatorConfig
	otelEnabled      bool
	middlewares      []func(http.Handler) http.Handler
	customRoutes     map[string]http.Handler
	containerRuntime runtime.Runtime
	clientManager    client.Manager
	workloadManager  workloads.Manager
	groupManager     groups.Manager
	skillManager     skills.SkillService
	skillStoreCloser io.Closer
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

// WithNonce sets the server instance nonce used for discovery verification.
// When non-empty, the server writes a discovery file on startup and returns
// the nonce in the X-Toolhive-Nonce health check header.
func (b *ServerBuilder) WithNonce(nonce string) *ServerBuilder {
	b.nonce = nonce
	return b
}

// WithOIDCConfig sets the OIDC configuration
func (b *ServerBuilder) WithOIDCConfig(oidcConfig *auth.TokenValidatorConfig) *ServerBuilder {
	b.oidcConfig = oidcConfig
	return b
}

// WithOtelEnabled enables OTEL HTTP middleware for distributed tracing.
// When enabled, the server extracts W3C traceparent headers from incoming requests
// and creates child OTEL spans for each request. Requires OTEL to be initialized
// (via telemetry.NewProvider) before the server starts.
func (b *ServerBuilder) WithOtelEnabled(enabled bool) *ServerBuilder {
	b.otelEnabled = enabled
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

// WithSkillManager sets the skill service manager.
// The caller is responsible for closing any underlying resources
// when providing an external skill service.
func (b *ServerBuilder) WithSkillManager(manager skills.SkillService) *ServerBuilder {
	b.skillManager = manager
	return b
}

// Build creates and configures the HTTP router
func (b *ServerBuilder) Build(ctx context.Context) (*chi.Mux, error) {
	r := chi.NewRouter()

	// OTEL middleware must be outermost so its span is still active when recovery
	// middleware catches a panic. If recovery were outer, otelhttp's defer span.End()
	// would fire during panic unwinding — before recover() — leaving the span ended
	// and making span.RecordError a no-op. With otelhttp outer:
	//   1. otelhttp starts span with a provisional name, calls next
	//   2. chiRouteTagMiddleware renames the span after routing has resolved
	//   3. recovery catches any panic, calls span.RecordError, returns 500 normally
	//   4. otelhttp's defer fires: span has error recorded + 500 status, then ends
	//
	// Note: otelhttp reads W3C traceparent/tracestate headers before authentication.
	// Untrusted clients can inject trace IDs or set sampled=1 to influence sampling.
	// The ParentBased sampler (in otlp/tracing.go) partially mitigates forced sampling
	// by delegating root decisions to TraceIDRatioBased.
	if b.otelEnabled {
		r.Use(otelhttp.NewMiddleware("thv-api"))
		// chiRouteTagMiddleware runs after routing so RoutePattern() is populated.
		// It renames the span from the provisional "thv-api" to e.g.
		// "GET /api/v1beta/workloads/{name}" for clean grouping in OTEL backends.
		r.Use(chiRouteSpanNamer)
	}

	// Recovery middleware is inner so it runs inside the OTEL span lifetime,
	// allowing panic details to be recorded on the span before it ends.
	r.Use(recovery.Middleware)

	// Apply default middleware
	// NOTE: Timeout is NOT applied globally because workload create/update routes
	// pull container images, which can take minutes. Instead, timeouts are applied
	// per-route group in setupDefaultRoutes and within WorkloadRouter.
	r.Use(
		middleware.RequestID,
		// TODO: Figure out logging middleware. We may want to use a different logger.
		requestBodySizeLimitMiddleware(maxRequestBodySize),
		headersMiddleware,
	)

	// Add update check middleware
	r.Use(updateCheckMiddleware())

	// Add authentication middleware
	authMiddleware, _, err := auth.GetAuthenticationMiddleware(ctx, b.oidcConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create authentication middleware: %w", err)
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

	// Add custom routes (callers of WithRoute are responsible for their own timeout management)
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
			return fmt.Errorf("failed to create container runtime: %w", err)
		}
	}

	if b.clientManager == nil {
		b.clientManager, err = client.NewManager(ctx)
		if err != nil {
			return fmt.Errorf("failed to create client manager: %w", err)
		}
	}

	if b.workloadManager == nil {
		b.workloadManager, err = workloads.NewManagerFromRuntime(b.containerRuntime)
		if err != nil {
			return fmt.Errorf("failed to create workload manager: %w", err)
		}
	}

	if b.groupManager == nil {
		b.groupManager, err = groups.NewManager()
		if err != nil {
			return fmt.Errorf("failed to create group manager: %w", err)
		}
	}

	if b.skillManager == nil {
		store, storeErr := sqlite.NewDefaultSkillStore()
		if storeErr != nil {
			return fmt.Errorf("failed to create skill store: %w", storeErr)
		}
		b.skillStoreCloser = store
		cm, cmErr := client.NewClientManager()
		if cmErr != nil {
			_ = store.Close()
			return fmt.Errorf("failed to create client manager for skills: %w", cmErr)
		}

		ociStore, ociErr := ociskills.NewStore(ociskills.DefaultStoreRoot())
		if ociErr != nil {
			_ = store.Close()
			return fmt.Errorf("failed to create OCI skill store: %w", ociErr)
		}
		ociRegistry, regErr := newOCIRegistryClient()
		if regErr != nil {
			_ = store.Close()
			// ociStore is directory-backed with no open handles; no cleanup needed.
			return fmt.Errorf("failed to create OCI registry client: %w", regErr)
		}
		packager := ociskills.NewPackager(ociStore)

		skillOpts := []skillsvc.Option{
			skillsvc.WithPathResolver(&clientPathAdapter{cm: cm}),
			skillsvc.WithOCIStore(ociStore),
			skillsvc.WithPackager(packager),
			skillsvc.WithRegistryClient(ociRegistry),
			skillsvc.WithGroupManager(b.groupManager),
		}

		skillOpts = append(skillOpts,
			skillsvc.WithSkillLookup(lazySkillLookup{}),
			skillsvc.WithGitResolver(gitresolver.NewResolver()),
		)

		b.skillManager = skillsvc.New(store, skillOpts...)
	}

	return nil
}

// setupDefaultRoutes sets up the default API routes
func (b *ServerBuilder) setupDefaultRoutes(r *chi.Mux) {
	standardTimeout := middleware.Timeout(middlewareTimeout)

	// Workload router manages its own per-route timeouts (image pulls can take minutes)
	r.Mount("/api/v1beta/workloads", v1.WorkloadRouter(
		b.workloadManager,
		b.containerRuntime,
		b.groupManager,
		b.debugMode,
	))

	// All other routes get standard timeout
	standardRouters := map[string]http.Handler{
		"/health":               v1.HealthcheckRouter(b.containerRuntime, b.nonce),
		"/api/v1beta/version":   v1.VersionRouter(),
		"/api/v1beta/registry":  v1.RegistryRouter(true),
		"/api/v1beta/discovery": v1.DiscoveryRouter(),
		"/api/v1beta/clients":   v1.ClientRouter(b.clientManager, b.workloadManager, b.groupManager),
		"/api/v1beta/secrets":   v1.SecretsRouter(),
		"/api/v1beta/groups":    v1.GroupsRouter(b.groupManager, b.workloadManager, b.clientManager),
		"/api/v1beta/skills":    v1.SkillsRouter(b.skillManager),
		"/registry":             v1.RegistryV01Router(),
	}
	for prefix, router := range standardRouters {
		r.Mount(prefix, standardTimeout(router))
	}

	// Only mount docs router if enabled
	if b.enableDocs {
		r.Mount("/api/", standardTimeout(DocsRouter()))
	}
}

func setupTCPListener(address string) (net.Listener, error) {
	return net.Listen("tcp", address)
}

func setupUnixSocket(address string) (net.Listener, error) {
	// Remove the socket file if it already exists
	if _, err := os.Stat(address); err == nil {
		if err := os.Remove(address); err != nil {
			return nil, fmt.Errorf("failed to remove existing socket: %w", err)
		}
	}

	// Create the directory for the socket file if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(address), 0750); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	// Create UNIX socket listener
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, fmt.Errorf("failed to create UNIX socket listener: %w", err)
	}

	// Set file permissions on the socket to allow other local processes to connect
	if err := os.Chmod(address, socketPermissions); err != nil {
		return nil, fmt.Errorf("failed to set socket permissions: %w", err)
	}

	return listener, nil
}

func cleanupUnixSocket(address string) {
	if err := os.Remove(address); err != nil && !os.IsNotExist(err) {
		slog.Warn("failed to remove socket file", "error", err)
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
					//nolint:gosec // G706: component is an internal string constant
					slog.Warn("unable to create update client", "component", component, "error", err)
					return
				}

				err = updateChecker.CheckLatestVersion()
				if err != nil {
					//nolint:gosec // G706: component is an internal string constant
					slog.Warn("could not check for updates", "component", component, "error", err)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// maxBytesTracker wraps an io.ReadCloser to track bytes read and detect size limit violations
type maxBytesTracker struct {
	io.ReadCloser
	bytesRead     *int64
	limit         int64
	limitExceeded *bool
}

func (t *maxBytesTracker) Read(p []byte) (n int, err error) {
	n, err = t.ReadCloser.Read(p)
	*t.bytesRead += int64(n)

	// Check if we've reached/exceeded the limit or if this is a MaxBytesError
	// Use >= because MaxBytesReader stops AT the limit, not after it
	if *t.bytesRead >= t.limit {
		*t.limitExceeded = true
	}

	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			*t.limitExceeded = true
		}
	}

	return n, err
}

// bodySizeResponseWriter wraps http.ResponseWriter to convert 400 to 413 only when
// MaxBytesReader's limit was exceeded (not for validation errors)
type bodySizeResponseWriter struct {
	http.ResponseWriter
	limitExceeded *bool
	written       bool
}

func (w *bodySizeResponseWriter) WriteHeader(statusCode int) {
	// Only convert 400 to 413 if MaxBytesReader's limit was actually exceeded
	if statusCode == http.StatusBadRequest && !w.written && *w.limitExceeded {
		statusCode = http.StatusRequestEntityTooLarge
	}
	w.written = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *bodySizeResponseWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// requestBodySizeLimitMiddleware limits request body size, returns a 413 for request bodies larger than maxSize.
func requestBodySizeLimitMiddleware(maxSize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check Content-Length header first for early rejection
			if r.ContentLength > maxSize {
				slog.Warn("request body size exceeds limit", //nolint:gosec // G706: request metadata for diagnostics
					"content_length", r.ContentLength, "limit", maxSize, "method", r.Method, "path", r.URL.Path)
				http.Error(w, "Request Entity Too Large", http.StatusRequestEntityTooLarge)
				return
			}

			// Track if MaxBytesReader's limit is exceeded
			limitExceeded := false
			bytesRead := int64(0)

			// Wrap ResponseWriter to intercept only MaxBytesReader errors
			wrappedWriter := &bodySizeResponseWriter{
				ResponseWriter: w,
				limitExceeded:  &limitExceeded,
				written:        false,
			}

			// Set MaxBytesReader as a safety net for requests without Content-Length
			limitedBody := http.MaxBytesReader(wrappedWriter, r.Body, maxSize)

			// Wrap the limited body to detect when size limit is exceeded
			tracker := &maxBytesTracker{
				ReadCloser:    limitedBody,
				bytesRead:     &bytesRead,
				limit:         maxSize,
				limitExceeded: &limitExceeded,
			}
			r.Body = tracker

			next.ServeHTTP(wrappedWriter, r)
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
	nonce        string
	storeCloser  io.Closer
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
		nonce:        builder.nonce,
		storeCloser:  builder.skillStoreCloser,
	}, nil
}

// ListenURL returns the URL where the server is listening, using the actual
// bound address from the listener (important when binding to port 0).
func (s *Server) ListenURL() string {
	if s.isUnixSocket {
		return fmt.Sprintf("unix://%s", s.address)
	}
	return fmt.Sprintf("http://%s", s.listener.Addr().String())
}

// Start starts the server and blocks until the context is cancelled
func (s *Server) Start(ctx context.Context) error {
	slog.Info("starting server", "type", s.addrType, "address", s.address)

	// Write server discovery file so clients can find this instance.
	if err := s.writeDiscoveryFile(ctx); err != nil {
		return err
	}

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

// writeDiscoveryFile writes the server discovery file if a nonce is configured.
// It checks for an existing healthy server first to prevent silent orphaning.
// The entire check-then-write sequence is wrapped in a file lock to prevent
// TOCTOU races when two servers start simultaneously.
func (s *Server) writeDiscoveryFile(ctx context.Context) error {
	if s.nonce == "" {
		return nil
	}

	// Ensure the discovery directory exists before acquiring the lock,
	// since the lock file is created in the same directory.
	discoveryPath := discovery.FilePath()
	if err := os.MkdirAll(filepath.Dir(discoveryPath), 0700); err != nil {
		return fmt.Errorf("failed to create discovery directory: %w", err)
	}

	return fileutils.WithFileLock(discoveryPath, func() error {
		// Guard against overwriting another server's discovery file.
		result, err := discovery.Discover(ctx)
		if err != nil {
			slog.Debug("discovery check failed, proceeding with startup", "error", err)
		} else {
			switch result.State {
			case discovery.StateRunning:
				return fmt.Errorf("another ToolHive server is already running at %s (PID %d)", result.Info.URL, result.Info.PID)
			case discovery.StateStale:
				slog.Debug("cleaning up stale discovery file", "pid", result.Info.PID)
				if err := discovery.CleanupStale(); err != nil {
					slog.Warn("failed to clean up stale discovery file", "error", err)
				}
			case discovery.StateUnhealthy:
				// The process is alive but not responding to health checks.
				// This can happen after a crash-restart where the old process
				// is hung. We intentionally overwrite the discovery file so
				// this new server becomes discoverable.
				slog.Warn("existing server is unhealthy, overwriting discovery file", "pid", result.Info.PID)
			case discovery.StateNotFound:
				// No existing server, proceed normally.
			}
		}

		info := &discovery.ServerInfo{
			URL:       s.ListenURL(),
			PID:       os.Getpid(),
			Nonce:     s.nonce,
			StartedAt: time.Now().UTC(),
		}
		if err := discovery.WriteServerInfo(info); err != nil {
			return fmt.Errorf("failed to write discovery file: %w", err)
		}
		slog.Debug("wrote discovery file", "url", info.URL, "pid", info.PID)
		return nil
	})
}

// shutdown gracefully shuts down the server
func (s *Server) shutdown() error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := s.httpServer.Shutdown(shutdownCtx); err != nil {
		s.cleanup()
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	s.cleanup()
	slog.Debug("server stopped", "type", s.addrType)
	return nil
}

// cleanup performs cleanup operations
func (s *Server) cleanup() {
	if s.nonce != "" {
		if err := discovery.RemoveServerInfo(); err != nil {
			slog.Warn("failed to remove discovery file", "error", err)
		}
	}
	if s.storeCloser != nil {
		if err := s.storeCloser.Close(); err != nil {
			slog.Warn("failed to close skill store", "error", err)
		}
	}
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

// newOCIRegistryClient creates an OCI registry client. In dev mode
// (TOOLHIVE_DEV=true), plain HTTP is enabled for local test registries.
func newOCIRegistryClient() (ociskills.RegistryClient, error) {
	var opts []ociskills.RegistryOption
	if os.Getenv("TOOLHIVE_DEV") == "true" {
		opts = append(opts, ociskills.WithPlainHTTP(true))
	}
	return ociskills.NewRegistry(opts...)
}

// lazySkillLookup implements skillsvc.SkillLookup by resolving the registry
// provider on each call. This ensures that registry config changes (via
// thv config set-registry or the API) are picked up without restarting
// the server, because ResetDefaultProvider clears the cached provider and
// the next GetDefaultProviderWithConfig call creates a fresh one.
type lazySkillLookup struct{}

func (lazySkillLookup) SearchSkills(query string) ([]regtypes.Skill, error) {
	provider, err := registry.GetDefaultProviderWithConfig(config.NewDefaultProvider())
	if err != nil {
		return nil, err
	}
	return provider.SearchSkills(query)
}

// clientPathAdapter adapts *client.ClientManager to the skills.PathResolver interface.
type clientPathAdapter struct {
	cm *client.ClientManager
}

func (a *clientPathAdapter) GetSkillPath(clientType, skillName string, scope skills.Scope, projectRoot string) (string, error) {
	return a.cm.GetSkillPath(client.ClientApp(clientType), skillName, scope, projectRoot)
}

func (a *clientPathAdapter) ListSkillSupportingClients() []string {
	clients := a.cm.ListSkillSupportingClients()
	var result []string
	for _, c := range clients {
		if a.cm.IsClientInstalled(c) {
			result = append(result, string(c))
		} else {
			slog.Debug("skipping client for skill install: not detected on system", "client", c)
		}
	}
	return result
}

// chiRouteSpanNamer is a middleware that renames the active OTEL span to reflect
// the matched chi route pattern (e.g. "GET /api/v1beta/workloads/{name}") and
// records each URL path parameter as a span attribute for drill-down visibility.
//
// otelhttp creates the span with a provisional name at request start, before
// chi has matched the route. This middleware runs after chi routing completes
// (i.e. it wraps next.ServeHTTP and renames the span on the way back up), so
// RouteContext.RoutePattern() is guaranteed to be populated.
//
// Low-cardinality span names group spans in OTEL/Sentry backends; the path
// parameter attributes (e.g. url.path_param.name="my-server") retain the
// concrete values for trace-level debugging without inflating cardinality.
func chiRouteSpanNamer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		rctx := chi.RouteContext(r.Context())
		if rctx == nil || rctx.RoutePattern() == "" {
			return
		}
		span := trace.SpanFromContext(r.Context())
		span.SetName(r.Method + " " + rctx.RoutePattern())
		// Add each matched URL parameter as a span attribute so the actual
		// value (e.g. the workload/MCP name) is visible in the trace without
		// raising span-name cardinality.
		attrs := make([]attribute.KeyValue, 0, len(rctx.URLParams.Keys))
		for i, key := range rctx.URLParams.Keys {
			attrs = append(attrs, attribute.String("url.path_param."+key, rctx.URLParams.Values[i]))
		}
		if len(attrs) > 0 {
			span.SetAttributes(attrs...)
		}
	})
}

// GenerateNonce generates a random nonce for server instance identification.
func GenerateNonce() (string, error) {
	b := make([]byte, nonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate server nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Serve starts the server on the given address and serves the API.
// It is assumed that the caller sets up appropriate signal handling.
// If isUnixSocket is true, address is treated as a UNIX socket path.
// If oidcConfig is provided, OIDC authentication will be enabled for all API endpoints.
// Serve is a convenience wrapper that builds and starts the API server.
// For callers that need to configure OTEL or other builder options not exposed
// here, use NewServerBuilder and NewServer directly.
func Serve(
	ctx context.Context,
	address string,
	isUnixSocket bool,
	debugMode bool,
	enableDocs bool,
	oidcConfig *auth.TokenValidatorConfig,
	middlewares ...func(http.Handler) http.Handler,
) error {
	nonce, err := GenerateNonce()
	if err != nil {
		return err
	}

	builder := NewServerBuilder().
		WithAddress(address).
		WithUnixSocket(isUnixSocket).
		WithDebugMode(debugMode).
		WithDocs(enableDocs).
		WithNonce(nonce).
		WithOIDCConfig(oidcConfig).
		WithMiddleware(middlewares...)

	server, err := NewServer(ctx, builder)
	if err != nil {
		return err
	}

	return server.Start(ctx)
}
