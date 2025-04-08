package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/logger"
)

// HTTPEgressProxy implements the Proxy interface for egress control
type HTTPEgressProxy struct {
	config *Config
	server *http.Server
	mutex  sync.Mutex
}

// NewHTTPEgressProxy creates a new HTTP egress proxy
func NewHTTPEgressProxy(config *Config) *HTTPEgressProxy {
	return &HTTPEgressProxy{
		config: config,
	}
}

// Start starts the egress proxy
func (p *HTTPEgressProxy) Start(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Create server
	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.config.Host, p.config.Port),
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start server in a goroutine
	go func() {
		logger.Log.Info(fmt.Sprintf("Starting egress proxy %s on %s", p.config.Name, p.server.Addr))
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Log.Error(fmt.Sprintf("Egress proxy error: %v", err))
		}
	}()

	// Monitor context for cancellation
	go func() {
		<-ctx.Done()
		logger.Log.Info(fmt.Sprintf("Context done, stopping egress proxy %s", p.config.Name))
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.server.Shutdown(shutdownCtx); err != nil {
			logger.Log.Error(fmt.Sprintf("Error shutting down egress proxy: %v", err))
		}
	}()

	return nil
}

// Stop stops the egress proxy
func (p *HTTPEgressProxy) Stop(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	if p.server != nil {
		logger.Log.Info(fmt.Sprintf("Stopping egress proxy %s", p.config.Name))
		if err := p.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to shutdown egress proxy: %w", err)
		}
		p.server = nil
	}

	return nil
}

// ServeHTTP handles HTTP requests
func (p *HTTPEgressProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Check if the request is allowed
	if !p.isRequestAllowed(r) {
		logger.Log.Warn(fmt.Sprintf("Blocked egress request to %s", r.URL.String()))
		http.Error(w, "Egress request blocked by policy", http.StatusForbidden)
		return
	}

	// Forward the request
	p.forwardRequest(w, r)
}

// isRequestAllowed checks if a request is allowed by the egress policy
func (p *HTTPEgressProxy) isRequestAllowed(r *http.Request) bool {
	// If no restrictions are set, allow all
	if len(p.config.AllowedHosts) == 0 &&
		len(p.config.AllowedPorts) == 0 &&
		len(p.config.AllowedTransports) == 0 {
		return true
	}

	// Check all restrictions
	if !p.isHostAllowed(r) {
		return false
	}

	if !p.isPortAllowed(r) {
		return false
	}

	if !p.isTransportAllowed(r) {
		return false
	}

	return true
}

// isHostAllowed checks if the host in the request is allowed
func (p *HTTPEgressProxy) isHostAllowed(r *http.Request) bool {
	// If no host restrictions, allow all hosts
	if len(p.config.AllowedHosts) == 0 {
		return true
	}

	requestHost := r.URL.Hostname()
	for _, host := range p.config.AllowedHosts {
		// Allow exact matches
		if requestHost == host {
			return true
		}

		// Allow wildcard matches (*.example.com)
		if strings.HasPrefix(host, "*.") {
			domain := host[2:] // Remove the "*." prefix
			if strings.HasSuffix(requestHost, domain) {
				return true
			}
		}
	}

	return false
}

// isPortAllowed checks if the port in the request is allowed
func (p *HTTPEgressProxy) isPortAllowed(r *http.Request) bool {
	// If no port restrictions, allow all ports
	if len(p.config.AllowedPorts) == 0 {
		return true
	}

	port := r.URL.Port()
	if port == "" {
		// Default ports
		if r.URL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	for _, allowedPort := range p.config.AllowedPorts {
		if fmt.Sprintf("%d", allowedPort) == port {
			return true
		}
	}

	return false
}

// isTransportAllowed checks if the transport in the request is allowed
func (p *HTTPEgressProxy) isTransportAllowed(r *http.Request) bool {
	// If no transport restrictions, allow all transports
	if len(p.config.AllowedTransports) == 0 {
		return true
	}

	for _, t := range p.config.AllowedTransports {
		if r.URL.Scheme == t {
			return true
		}
	}

	return false
}

// forwardRequest forwards an HTTP request to its destination
func (*HTTPEgressProxy) forwardRequest(w http.ResponseWriter, r *http.Request) {
	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(r.URL)

	// Update the headers to allow for SSL redirection
	r.Header.Set("X-Forwarded-Host", r.Host)
	r.Header.Set("X-Forwarded-Proto", r.URL.Scheme)
	r.Header.Set("X-Forwarded-For", r.RemoteAddr)

	// Log the request
	logger.Log.Info(fmt.Sprintf("Forwarding egress request to %s", r.URL.String()))

	// Forward the request
	proxy.ServeHTTP(w, r)
}

// GetPort returns the port the proxy is listening on
func (p *HTTPEgressProxy) GetPort() int {
	if p.server == nil {
		return p.config.Port
	}

	// Extract port from server address
	_, portStr, err := net.SplitHostPort(p.server.Addr)
	if err != nil {
		return p.config.Port
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return p.config.Port
	}

	return port
}

// GetHost returns the host the proxy is bound to
func (p *HTTPEgressProxy) GetHost() string {
	if p.server == nil {
		return p.config.Host
	}

	// Extract host from server address
	host, _, err := net.SplitHostPort(p.server.Addr)
	if err != nil {
		return p.config.Host
	}

	return host
}
