package common

import (
	"fmt"
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
)

// ServerConfig holds configuration for creating an HTTP server.
type ServerConfig struct {
	Host              string
	Port              int
	Handler           http.Handler
	ReadHeaderTimeout time.Duration
}

// DefaultReadHeaderTimeout is the default timeout for reading request headers.
const DefaultReadHeaderTimeout = 10 * time.Second

// NewHTTPServer creates a new HTTP server with standard security settings.
func NewHTTPServer(config ServerConfig) *http.Server {
	if config.ReadHeaderTimeout == 0 {
		config.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}

	return &http.Server{
		Addr:              fmt.Sprintf("%s:%d", config.Host, config.Port),
		Handler:           config.Handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
	}
}

// MountHealthCheck adds a health check endpoint to the mux without middlewares.
func MountHealthCheck(mux *http.ServeMux, healthChecker http.Handler) {
	if healthChecker != nil {
		mux.Handle("/health", healthChecker)
	}
}

// MountMetrics adds a Prometheus metrics endpoint to the mux without middlewares.
// Returns true if the handler was non-nil and mounted.
func MountMetrics(mux *http.ServeMux, metricsHandler http.Handler) bool {
	if metricsHandler != nil {
		mux.Handle("/metrics", metricsHandler)
		logger.Info("Prometheus metrics endpoint enabled at /metrics")
		return true
	}
	return false
}
