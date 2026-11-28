// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mcpserver "github.com/stacklok/toolhive-core/mcpcompat/server"
	transportsession "github.com/stacklok/toolhive/pkg/transport/session"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/optimizer"
	"github.com/stacklok/toolhive/pkg/vmcp/server/sessionmanager"
)

func TestScopeResponseProfileSelection(t *testing.T) {
	t.Parallel()
	known := ScopeClientProfile{Name: "fixture-client", Version: "1.2.3"}
	for _, tc := range []struct {
		name string
		mode string
		info map[string]interface{}
		tool bool
	}{
		{"default stays http", "", map[string]interface{}{"name": known.Name, "version": known.Version}, false},
		{"forced http", "http", map[string]interface{}{"name": known.Name, "version": known.Version}, false},
		{"forced tool result ignores unknown", "tool-result", nil, true},
		{"auto exact profile", "auto", map[string]interface{}{"name": known.Name, "version": known.Version}, true},
		{"auto missing", "auto", nil, false},
		{"auto unknown name", "auto", map[string]interface{}{"name": "other", "version": known.Version}, false},
		{"auto unknown version", "auto", map[string]interface{}{"name": known.Name, "version": "1.2.4"}, false},
		{"auto case sensitive", "auto", map[string]interface{}{"name": "FIXTURE-CLIENT", "version": known.Version}, false},
		{"auto malformed name", "auto", map[string]interface{}{"name": 7, "version": known.Version}, false},
		{"auto malformed version", "auto", map[string]interface{}{"name": known.Name, "version": []string{known.Version}}, false},
		{"auto blank", "auto", map[string]interface{}{"name": " ", "version": known.Version}, false},
		{"auto overlong", "auto", map[string]interface{}{"name": strings.Repeat("x", 129), "version": known.Version}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &ScopeChallengeConfig{ResponseMode: tc.mode, ToolResultClients: []ScopeClientProfile{known}}
			require.Equal(t, tc.tool, cfg.toolResult(scopeClientInfo(tc.info)))
		})
	}
}

func TestScopeResponseConfigOwnership(t *testing.T) {
	t.Parallel()
	cfg := &ScopeChallengeConfig{ResponseMode: "auto", ToolResultClients: []ScopeClientProfile{{Name: "fixture", Version: "1"}}}
	policy := &vmcp.ScopeAuthorizationConfig{Tools: map[string]vmcp.OAuthScopeSet{"echo": {"write"}}}
	cloned := buildServeConfig(&ServerConfig{ScopeAuthorization: policy, ScopeChallenge: cfg})
	cfg.ToolResultClients[0].Name = "changed"
	policy.Tools["echo"][0] = "changed"
	require.Equal(t, "fixture", cloned.ScopeChallenge.ToolResultClients[0].Name)
	require.Equal(t, "write", cloned.ScopeAuthorization.Tools["echo"][0])
	var disabled *ScopeChallengeConfig
	require.Nil(t, disabled.clone())
}

func TestScopeResponseStartupValidation(t *testing.T) {
	t.Parallel()
	var optimizerFactoryCalls atomic.Int32
	t.Cleanup(func() {
		require.Zero(t, optimizerFactoryCalls.Load(), "startup validation must not invoke the optimizer factory")
	})
	for _, tc := range []struct {
		name    string
		change  func(*ServerConfig)
		message string
	}{
		{"missing policy", func(c *ServerConfig) { c.ScopeAuthorization = nil }, "both required"},
		{"missing renderer", func(c *ServerConfig) { c.ScopeChallenge = nil }, "both required"},
		{"invalid policy", func(c *ServerConfig) { c.ScopeAuthorization.Tools = nil }, "scope authorization requires"},
		{"missing authentication", func(c *ServerConfig) { c.AuthMiddleware = nil }, "authentication"},
		{"missing metadata", func(c *ServerConfig) { c.AuthInfoHandler = nil }, "protected resource metadata"},
		{"invalid resource", func(c *ServerConfig) { c.ScopeChallenge.ResourceURL = "opaque-audience" }, "canonical HTTP(S)"},
		{"invalid mode", func(c *ServerConfig) { c.ScopeChallenge.ResponseMode = "guess" }, "response mode"},
		{"optimizer", func(c *ServerConfig) { c.SessionManagerConfig.OptimizerConfig = &optimizer.Config{} }, "optimizer"},
		{"optimizer factory", func(c *ServerConfig) {
			c.SessionManagerConfig.OptimizerFactory = func(context.Context, []mcpserver.ServerTool) (optimizer.Optimizer, error) {
				optimizerFactoryCalls.Add(1)
				return nil, nil
			}
		}, "optimizer"},
		{"invalid profile", func(c *ServerConfig) { c.ScopeChallenge.ToolResultClients = []ScopeClientProfile{{Name: "fixture"}} }, "profiles"},
		{"duplicate profile", func(c *ServerConfig) {
			c.ScopeChallenge.ToolResultClients = []ScopeClientProfile{{Name: "fixture", Version: "1"}, {Name: "fixture", Version: "1"}}
		}, "distinct"},
		{"overlong profile", func(c *ServerConfig) {
			c.ScopeChallenge.ToolResultClients = []ScopeClientProfile{{Name: strings.Repeat("x", 129), Version: "1"}}
		}, "profiles"},
		{"too many profiles", func(c *ServerConfig) { c.ScopeChallenge.ToolResultClients = make([]ScopeClientProfile, 65) }, "at most 64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := testMinimalServeConfig()
			cfg.ScopeAuthorization = &vmcp.ScopeAuthorizationConfig{Tools: map[string]vmcp.OAuthScopeSet{"echo": {"write"}}}
			cfg.ScopeChallenge = &ScopeChallengeConfig{ResourceURL: "https://gateway.example/mcp", ResponseMode: "auto"}
			cfg.AuthMiddleware = func(next http.Handler) http.Handler { return next }
			cfg.AuthInfoHandler = http.NewServeMux()
			tc.change(cfg)
			_, err := Serve(t.Context(), &modernFakeCore{}, cfg)
			require.ErrorIs(t, err, vmcp.ErrInvalidConfig)
			require.ErrorContains(t, err, tc.message)
		})
	}
}

func TestScopeResponseLegacyMetadataRead(t *testing.T) {
	t.Parallel()
	storage, err := transportsession.NewLocalSessionDataStorage(time.Hour)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, storage.Close()) })
	_, err = storage.Create(t.Context(), "known", map[string]string{
		sessionmanager.MetadataKeyClientName: "fixture", sessionmanager.MetadataKeyClientVersion: "1",
	})
	require.NoError(t, err)
	_, err = storage.Create(t.Context(), "terminated", map[string]string{sessionmanager.MetadataKeyTerminated: sessionmanager.MetadataValTrue})
	require.NoError(t, err)
	s := &Server{sessionDataStorage: storage, config: &Config{ScopeChallenge: &ScopeChallengeConfig{ResponseMode: "auto"}}}
	for _, tc := range []struct {
		id   string
		want ScopeClientProfile
		err  error
	}{
		{"known", ScopeClientProfile{Name: "fixture", Version: "1"}, nil},
		{"", ScopeClientProfile{}, nil},
		{"absent", ScopeClientProfile{}, transportsession.ErrSessionNotFound},
		{"terminated", ScopeClientProfile{}, transportsession.ErrSessionNotFound},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
			if tc.id != "" {
				req.Header.Set("Mcp-Session-Id", tc.id)
			}
			got, err := s.legacyScopeClient(req)
			require.ErrorIs(t, err, tc.err)
			require.Equal(t, tc.want, got)
		})
	}
	// A format decision does not need a live backend session or the session
	// manager: only persisted metadata is read here.
	require.Nil(t, s.vmcpSessionMgr)
}
