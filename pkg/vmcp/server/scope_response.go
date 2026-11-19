// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
	sessiontypes "github.com/stacklok/toolhive/pkg/vmcp/session/types"
)

// ScopeClientProfile identifies one explicitly verified client implementation.
// It selects response encoding only and never grants authorization.
type ScopeClientProfile struct{ Name, Version string }

// ScopeChallengeConfig controls incoming scope-refusal encoding independently
// of the core's authorization policy. Auto recognizes exact configured profiles;
// missing or unknown client information falls back to standard HTTP challenges.
type ScopeChallengeConfig struct {
	ResourceURL string
	// ResponseMode is http (default), tool-result, or auto.
	ResponseMode      string
	ToolResultClients []ScopeClientProfile
}

func (c *ScopeChallengeConfig) clone() *ScopeChallengeConfig {
	if c == nil {
		return nil
	}
	cloned := *c
	cloned.ToolResultClients = slices.Clone(c.ToolResultClients)
	return &cloned
}

func (c *ScopeChallengeConfig) toolResult(client ScopeClientProfile) bool {
	return c.ResponseMode == "tool-result" ||
		(c.ResponseMode == "auto" && validScopeClient(client) && slices.Contains(c.ToolResultClients, client))
}

func validScopeClient(client ScopeClientProfile) bool {
	return strings.TrimSpace(client.Name) != "" && len(client.Name) <= 128 &&
		strings.TrimSpace(client.Version) != "" && len(client.Version) <= 128
}

func scopeClientInfo(info map[string]interface{}) ScopeClientProfile {
	name, _ := info["name"].(string)
	version, _ := info["version"].(string)
	client := ScopeClientProfile{Name: name, Version: version}
	if !validScopeClient(client) {
		return ScopeClientProfile{}
	}
	return client
}

func validateScopeServing(cfg *ServerConfig) error {
	if cfg.ScopeAuthorization == nil && cfg.ScopeChallenge == nil {
		return nil
	}
	if cfg.ScopeAuthorization == nil || cfg.ScopeChallenge == nil {
		return fmt.Errorf("%w: scope policy and challenge configuration are both required", vmcp.ErrInvalidConfig)
	}
	if err := cfg.ScopeAuthorization.Validate(); err != nil {
		return fmt.Errorf("%w: %w", vmcp.ErrInvalidConfig, err)
	}
	if cfg.AuthMiddleware == nil || cfg.AuthInfoHandler == nil {
		return fmt.Errorf("%w: scope challenges require authentication and protected resource metadata", vmcp.ErrInvalidConfig)
	}
	if cfg.SessionManagerConfig.OptimizerConfig != nil || cfg.SessionManagerConfig.OptimizerFactory != nil {
		return fmt.Errorf("%w: scope challenges do not support optimizer", vmcp.ErrInvalidConfig)
	}
	if _, err := scopeMetadataURL(cfg.ScopeChallenge.ResourceURL); err != nil {
		return fmt.Errorf("%w: %w", vmcp.ErrInvalidConfig, err)
	}
	return cfg.ScopeChallenge.validate()
}

func (c *ScopeChallengeConfig) validate() error {
	switch c.ResponseMode {
	case "", "http", "tool-result", "auto":
	default:
		return fmt.Errorf("%w: response mode must be http, tool-result, or auto", vmcp.ErrInvalidConfig)
	}
	if len(c.ToolResultClients) > 64 {
		return fmt.Errorf("%w: at most 64 scope client profiles are allowed", vmcp.ErrInvalidConfig)
	}
	seen := map[ScopeClientProfile]bool{}
	for _, client := range c.ToolResultClients {
		if !validScopeClient(client) || seen[client] {
			return fmt.Errorf("%w: scope client profiles require distinct names and versions of 1–128 bytes", vmcp.ErrInvalidConfig)
		}
		seen[client] = true
	}
	return nil
}

// captureScopeClient runs on the new, unshared session before the manager
// persists and caches it. Restores retain their stored profile instead.
func captureScopeClient(ctx context.Context, sess sessiontypes.MultiSession) (sessiontypes.MultiSession, error) {
	parsed := mcpparser.GetParsedMCPRequest(ctx)
	if parsed != nil && parsed.Method == "initialize" {
		info, _ := parsed.Arguments["clientInfo"].(map[string]interface{})
		client := scopeClientInfo(info)
		if client != (ScopeClientProfile{}) {
			sess.SetMetadata(sessionmanager.MetadataKeyClientName, client.Name)
			sess.SetMetadata(sessionmanager.MetadataKeyClientVersion, client.Version)
		}
	}
	return sess, nil
}

// legacyScopeClient runs after ownership, without backend restoration or a TTL refresh.
func (s *Server) legacyScopeClient(r *http.Request) (ScopeClientProfile, error) {
	if s.config.ScopeChallenge.ResponseMode != "auto" {
		return ScopeClientProfile{}, nil
	}
	id, err := sessionbinding.RequestID(r, "")
	if err != nil || id == "" {
		return ScopeClientProfile{}, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	metadata, err := s.sessionDataStorage.LoadMetadata(ctx, id)
	if err != nil {
		return ScopeClientProfile{}, err
	}
	if metadata[sessionmanager.MetadataKeyTerminated] == sessionmanager.MetadataValTrue {
		return ScopeClientProfile{}, transportsession.ErrSessionNotFound
	}
	return scopeClientInfo(map[string]interface{}{
		"name": metadata[sessionmanager.MetadataKeyClientName], "version": metadata[sessionmanager.MetadataKeyClientVersion],
	}), nil
}
