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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	v1 "github.com/stacklok/toolhive/pkg/api/v1"
	"github.com/stacklok/toolhive/pkg/container"
	"github.com/stacklok/toolhive/pkg/lifecycle"
	"github.com/stacklok/toolhive/pkg/logger"
)

// Not sure if these values need to be configurable.
const (
	middlewareTimeout = 60 * time.Second
	readHeaderTimeout = 10 * time.Second
	socketPermissions = 0660 // Socket file permissions (owner/group read-write)
)

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

// Serve starts the server on the given address and serves the API.
// It is assumed that the caller sets up appropriate signal handling.
// If isUnixSocket is true, address is treated as a UNIX socket path.
func Serve(ctx context.Context, address string, isUnixSocket bool, debugMode bool, enableDocs bool) error {
	r := chi.NewRouter()
	r.Use(
		middleware.RequestID,
		// TODO: Figure out logging middleware. We may want to use a different logger.
		middleware.Timeout(middlewareTimeout),
	)

	manager, err := lifecycle.NewManager(ctx)
	if err != nil {
		logger.Panicf("failed to create lifecycle manager: %v", err)
	}

	// Create container runtime
	rt, err := container.NewFactory().Create(ctx)
	if err != nil {
		return fmt.Errorf("failed to create container runtime: %v", err)
	}

	routers := map[string]http.Handler{
		"/health":             v1.HealthcheckRouter(),
		"/api/v1beta/version": v1.VersionRouter(),
		"/api/v1beta/servers": v1.ServerRouter(manager, rt, debugMode),
	}

	// Only mount docs router if enabled
	if enableDocs {
		routers["/api/"] = DocsRouter()
	}

	for prefix, router := range routers {
		r.Mount(prefix, router)
	}

	srv := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Addr:              address,
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Create a listener based on the connection type
	var listener net.Listener
	var addrType string

	if isUnixSocket {
		listener, err = setupUnixSocket(address)
		addrType = "UNIX socket"
	} else {
		listener, err = setupTCPListener(address)
		addrType = "HTTP"
	}
	if err != nil {
		return err
	}

	logger.Infof("starting %s server", addrType, address)

	// Start server.
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Panicf("server stopped with error: %v", err)
		}
	}()

	<-ctx.Done()
	if err := srv.Shutdown(ctx); err != nil {
		if isUnixSocket {
			cleanupUnixSocket(address)
		}
		return fmt.Errorf("server shutdown failed: %w", err)
	}

	if isUnixSocket {
		cleanupUnixSocket(address)
	}

	logger.Infof("%s server stopped", addrType)
	return nil
}
