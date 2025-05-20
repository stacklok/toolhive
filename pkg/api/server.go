// Package api contains the REST API for ToolHive.
package api

// The OpenAPI spec is generated using "github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc4"
// To update the OpenAPI spec, run:
// install swag:
//	go install github.com/swaggo/swag/v2/cmd/swag@v2.0.0-rc4
// generate the spec:
//	swag init -g pkg/api/server.go --v3.1

// @title           ToolHive API
// @version         1.0
// @description     This is the ToolHive API server.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
)

// Serve starts the HTTP server on the given address and serves the API.
// It is assumed that the caller sets up appropriate signal handling.
func Serve(ctx context.Context, address string, debugMode bool) error {
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
		"/api/":               DocsRouter(),
		"/api/v1beta/version": v1.VersionRouter(),
		"/api/v1beta/servers": v1.ServerRouter(manager, rt, debugMode),
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

	logger.Infof("starting http server on %s", srv.Addr)

	// Start server.
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Panicf("server stopped with error: %v", err)
		}
	}()

	// Kill server on context shutdown.
	<-ctx.Done()
	if err := srv.Shutdown(ctx); err != nil {
		return fmt.Errorf("server shutdown failed:%+v", err)
	}

	logger.Infof("http server stopped")
	return nil
}
