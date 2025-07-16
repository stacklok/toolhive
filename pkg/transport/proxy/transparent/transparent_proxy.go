// Package transparent provides a transparent HTTP proxy implementation
// that forwards requests to a destination without modifying them.
package transparent

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/exp/jsonrpc2"

	"github.com/stacklok/toolhive/pkg/healthcheck"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/transport/types"
)

// TransparentProxy implements the Proxy interface as a transparent HTTP proxy
// that forwards requests to a destination.
// It's used by the SSE transport to forward requests to the container's HTTP server.
//
//nolint:revive // Intentionally named TransparentProxy despite package name
type TransparentProxy struct {
	// Basic configuration
	host          string
	port          int
	containerName string
	targetURI     string

	// HTTP server
	server *http.Server

	// Middleware chain
	middlewares []types.Middleware

	// Mutex for protecting shared state
	mutex sync.Mutex

	// Shutdown channel
	shutdownCh chan struct{}

	// Health checker
	healthChecker *healthcheck.HealthChecker

	// Optional Prometheus metrics handler
	prometheusHandler http.Handler

	// Sessions for managing client connections
	sessions map[string]session.Session

	// mutex for protecting session access
	sessionMutex sync.Mutex
}

// TransparentProxySessionID is the session ID used for the transparent proxy.
const TransparentProxySessionID = "transparent-proxy-session"

// NewTransparentProxy creates a new transparent proxy with optional middlewares.
func NewTransparentProxy(
	host string,
	port int,
	containerName string,
	targetURI string,
	prometheusHandler http.Handler,
	middlewares ...types.Middleware,
) *TransparentProxy {
	proxy := &TransparentProxy{
		host:              host,
		port:              port,
		containerName:     containerName,
		targetURI:         targetURI,
		middlewares:       middlewares,
		shutdownCh:        make(chan struct{}),
		prometheusHandler: prometheusHandler,
		sessions:          make(map[string]session.Session),
	}

	// Create MCP pinger and health checker
	mcpPinger := NewMCPPinger(targetURI)
	proxy.healthChecker = healthcheck.NewHealthChecker("sse", mcpPinger)

	_, err := proxy.CreateSession(TransparentProxySessionID)
	if err != nil {
		logger.Errorf("Failed to create session for TransparentProxy: %v", err)
		return nil
	}

	return proxy
}

func (p *TransparentProxy) handleModifyResponse(res *http.Response) error {
	// Log headers
	if sid := res.Header.Get("Mcp-Session-Id"); sid != "" {
		logger.Infof("🆔 Streamable session ID from header: %s", sid)
	}

	// Handle streaming (SSE)
	if ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); ct == "text/event-stream" {
		pr, pw := io.Pipe()
		orig := res.Body
		res.Body = pr

		go func() {
			defer pw.Close()
			scanner := bufio.NewScanner(orig)
			re := regexp.MustCompile(`sessionId=([\w-]+)`) // Capture UUID-like IDs

			for scanner.Scan() {
				line := scanner.Text()

				if matches := re.FindStringSubmatch(line); len(matches) == 2 {
					sessionID := matches[1]

					// set session id for proxy
					extractedSession, ok := p.GetSession(TransparentProxySessionID)
					if !ok {
						logger.Errorf("Failed to get session for TransparentProxy")
						continue
					}
					extractedSession.SetIsInitialized(true)
					extractedSession.SetMCPSessionID(sessionID)
				}
				_, err := pw.Write([]byte(line + "\n"))
				if err != nil {
					logger.Errorf("Failed to write to pipe: %v", err)
				}
			}
		}()
		return nil
	}

	// Handle non-streaming (JSON) bodies
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	err = res.Body.Close()
	if err != nil {
		logger.Errorf("Failed to close response body: %v", err)
	}

	text := string(body)
	logger.Infof("HTTP response body: %s", text)

	// Parse sessionId if embedded in JSON
	rejson := regexp.MustCompile(`"sessionId"\s*:\s*"([\w-]+)"`)
	if matches := rejson.FindStringSubmatch(text); len(matches) == 2 {
		sessionID := matches[1]
		logger.Infof("🆔 Captured sessionId from JSON: %s", sessionID)
	}

	res.Body = io.NopCloser(bytes.NewReader(body))
	res.ContentLength = int64(len(body))
	res.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

func (p *TransparentProxy) handleAndDetectInitialize(w http.ResponseWriter, r *http.Request, proxy *httputil.ReverseProxy) {
	logger.Infof("Transparent proxy: %s %s -> %s", r.Method, r.URL.Path, p.targetURI)

	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mcp") {
		// Read the body for inspection without consuming it
		body, err := io.ReadAll(r.Body)
		if err != nil {
			logger.Errorf("Error reading request body: %v", err)
		} else {
			if bytes.Contains(body, []byte(`"method":"initialize"`)) {
				logger.Infof("🔧 Detected initialize request to %s", r.URL.Path)
				extractedSession, ok := p.GetSession(TransparentProxySessionID)
				if ok {
					extractedSession.SetIsInitialized(true)
				} else {
					logger.Errorf("No session found to mark initialized")
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))
		}
	}

	proxy.ServeHTTP(w, r)
}

// Start starts the transparent proxy.
func (p *TransparentProxy) Start(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Parse the target URI
	targetURL, err := url.Parse(p.targetURI)
	if err != nil {
		return fmt.Errorf("failed to parse target URI: %w", err)
	}

	// Create a reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.ModifyResponse = p.handleModifyResponse

	// Create a handler that logs requests
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.handleAndDetectInitialize(w, r, proxy)
	})

	// Create a mux to handle both proxy and health endpoints
	mux := http.NewServeMux()

	// Apply middleware chain in reverse order (last middleware is applied first)
	var finalHandler http.Handler = handler
	for i := len(p.middlewares) - 1; i >= 0; i-- {
		finalHandler = p.middlewares[i](finalHandler)
		logger.Infof("Applied middleware %d\n", i+1)
	}

	// Add the proxy handler for all paths except /health
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			// Health endpoint should not go through proxy
			http.NotFound(w, r)
			return
		}
		finalHandler.ServeHTTP(w, r)
	})

	// Add health check endpoint (no middlewares)
	mux.Handle("/health", p.healthChecker)

	// Add Prometheus metrics endpoint if handler is provided (no middlewares)
	if p.prometheusHandler != nil {
		mux.Handle("/metrics", p.prometheusHandler)
		logger.Info("Prometheus metrics endpoint enabled at /metrics")
	}

	// Create the server
	p.server = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", p.host, p.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // Prevent Slowloris attacks
	}

	// Start the server in a goroutine
	go func() {
		logger.Infof("Transparent proxy started for container %s on %s:%d -> %s",
			p.containerName, p.host, p.port, p.targetURI)

		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("Transparent proxy error: %v", err)
		}
	}()

	// Start health-check monitoring
	go p.monitorHealth(ctx)

	return nil
}

func (p *TransparentProxy) monitorHealth(parentCtx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-parentCtx.Done():
			logger.Infof("Context cancelled, stopping health monitor for %s", p.containerName)
			return
		case <-p.shutdownCh:
			logger.Infof("Shutdown initiated, stopping health monitor for %s", p.containerName)
			return
		case <-ticker.C:
			// Perform health check only if mcp server has been initialized
			extractedSession, isSession := p.GetSession(TransparentProxySessionID)
			if !isSession {
				logger.Errorf("Failed to get session for health check")
				return
			}

			if extractedSession.IsInitialized() {
				alive := p.healthChecker.CheckHealth(parentCtx)
				if alive.Status != healthcheck.StatusHealthy {
					logger.Infof("Health check failed for %s; initiating proxy shutdown", p.containerName)
					if err := p.Stop(parentCtx); err != nil {
						logger.Errorf("Failed to stop proxy for %s: %v", p.containerName, err)
					}
					return
				}
			} else {
				logger.Infof("Session %s is not initialized, cannot start healthcheck", extractedSession.ID())
			}
		}
	}
}

// Stop stops the transparent proxy.
func (p *TransparentProxy) Stop(ctx context.Context) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Signal shutdown
	close(p.shutdownCh)

	// Stop the HTTP server
	if p.server != nil {
		return p.server.Shutdown(ctx)
	}

	return nil
}

// IsRunning checks if the proxy is running.
func (p *TransparentProxy) IsRunning(_ context.Context) (bool, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Check if the shutdown channel is closed
	select {
	case <-p.shutdownCh:
		return false, nil
	default:
		return true, nil
	}
}

// GetMessageChannel returns the channel for messages to/from the destination.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (*TransparentProxy) GetMessageChannel() chan jsonrpc2.Message {
	return nil
}

// SendMessageToDestination sends a message to the destination.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (*TransparentProxy) SendMessageToDestination(_ jsonrpc2.Message) error {
	return fmt.Errorf("SendMessageToDestination not implemented for TransparentProxy")
}

// ForwardResponseToClients forwards a response from the destination to clients.
// This is not used in the TransparentProxy implementation as it forwards HTTP requests directly.
func (*TransparentProxy) ForwardResponseToClients(_ context.Context, _ jsonrpc2.Message) error {
	return fmt.Errorf("ForwardResponseToClients not implemented for TransparentProxy")
}

// CreateSession creates a new session for the transparent proxy.
func (p *TransparentProxy) CreateSession(id string) (session.Session, error) {
	logger.Infof("Creating session with ID: %s", id)
	if id == "" {
		return nil, fmt.Errorf("session ID cannot be empty")
	}
	p.sessionMutex.Lock()
	defer p.sessionMutex.Unlock()
	if _, found := p.sessions[id]; found {
		return nil, fmt.Errorf("session %s exists", id)
	}
	s := &session.ProxySession{Id: id}
	s.Init()
	p.sessions[id] = s
	return s, nil
}

// GetSession retrieves a session by ID.
func (p *TransparentProxy) GetSession(id string) (session.Session, bool) {
	logger.Infof("Retrieving session with ID: %s", id)
	if id == "" {
		return nil, false
	}
	p.sessionMutex.Lock()
	defer p.sessionMutex.Unlock()
	s, ok := p.sessions[id]
	return s, ok
}
