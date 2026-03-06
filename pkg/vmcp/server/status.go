// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/stacklok/toolhive/pkg/versions"
	"github.com/stacklok/toolhive/pkg/vmcp"
	authtypes "github.com/stacklok/toolhive/pkg/vmcp/auth/types"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

// StatusResponse represents the vMCP server's operational status.
type StatusResponse struct {
	Backends []BackendStatus `json:"backends"`
	Sessions *SessionsStatus `json:"sessions,omitempty"` // Only present when sessionManagementV2 enabled
	Healthy  bool            `json:"healthy"`
	Version  string          `json:"version"`
	GroupRef string          `json:"group_ref"`
}

// SessionsStatus contains information about active vMCP sessions.
type SessionsStatus struct {
	ActiveCount  int                                    `json:"active_count"`
	BackendUsage map[string]sessionmanager.BackendUsage `json:"backend_usage"` // backend_name -> usage stats
}

// BackendUsage tracks session usage statistics for a specific backend.
// This provides operational visibility without exposing session identifiers.
type BackendUsage = sessionmanager.BackendUsage

// BackendStatus represents the status of a single backend MCP server.
type BackendStatus struct {
	Name      string `json:"name"`
	Health    string `json:"health"`              // "healthy", "degraded", "unhealthy", "unknown"
	Transport string `json:"transport"`           // MCP transport protocol
	AuthType  string `json:"auth_type,omitempty"` // "unauthenticated", "header_injection", "token_exchange"
}

// handleStatus handles /status HTTP requests for operational visibility.
//
// Security Note: This endpoint is unauthenticated to support operator consumption
// and debugging. It exposes operational metadata (backend names, auth types)
// but NOT secrets, tokens, internal URLs, or request data. In sensitive multi-tenant
// deployments, restrict access to this endpoint via network policies.
//
// For minimal health checking (load balancers), use /health instead.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	response := s.buildStatusResponse(r.Context())

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Error("failed to encode status response", "error", err)
	}
}

// buildStatusResponse builds the StatusResponse from server state.
// Uses the provided context for request cancellation and tracing propagation.
func (s *Server) buildStatusResponse(ctx context.Context) StatusResponse {
	// Get current backends from registry (supports dynamic backend changes)
	backends := s.backendRegistry.List(ctx)
	backendStatuses := make([]BackendStatus, 0, len(backends))

	hasHealthyBackend := false
	for _, backend := range backends {
		status := BackendStatus{
			Name:      backend.Name,
			Health:    string(backend.HealthStatus),
			Transport: backend.TransportType,
			AuthType:  getAuthType(backend.AuthConfig),
		}
		backendStatuses = append(backendStatuses, status)

		if backend.HealthStatus == vmcp.BackendHealthy {
			hasHealthyBackend = true
		}
	}

	// Healthy = true if at least one backend is healthy AND there's at least one backend
	healthy := len(backends) > 0 && hasHealthyBackend

	response := StatusResponse{
		Backends: backendStatuses,
		Healthy:  healthy,
		Version:  versions.GetVersionInfo().Version,
		GroupRef: s.config.GroupRef,
	}

	// Add session information if sessionManagementV2 is enabled
	// This provides operational visibility into backend usage patterns without
	// exposing session identifiers that could be used for session hijacking.
	if s.config.SessionManagementV2 && s.vmcpSessionMgr != nil {
		activeCount, backendUsage := s.vmcpSessionMgr.ListActiveSessions()
		response.Sessions = &SessionsStatus{
			ActiveCount:  activeCount,
			BackendUsage: backendUsage,
		}
		// #nosec G706 -- structured logging with slog is safe from injection
		slog.Debug("buildStatusResponse: included session info",
			"active_sessions", activeCount,
			"backends", len(backendUsage),
			"v2_enabled", true)
	}

	return response
}

// getAuthType returns the auth type string from the backend auth strategy.
// Returns "unauthenticated" if the config is nil.
func getAuthType(cfg *authtypes.BackendAuthStrategy) string {
	if cfg == nil {
		return authtypes.StrategyTypeUnauthenticated
	}
	return cfg.Type
}
