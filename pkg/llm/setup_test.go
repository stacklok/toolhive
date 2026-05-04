// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/llmgateway"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// ── mergeToolConfigs ──────────────────────────────────────────────────────────

func TestMergeToolConfigs_EmptyExisting(t *testing.T) {
	t.Parallel()
	incoming := []ToolConfig{{Tool: "claude-code", Mode: "direct", ConfigPath: "/a"}}
	got := mergeToolConfigs(nil, incoming)
	assert.Equal(t, incoming, got)
}

func TestMergeToolConfigs_AppendsNew(t *testing.T) {
	t.Parallel()
	existing := []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/c"}}
	incoming := []ToolConfig{{Tool: "claude-code", Mode: "direct", ConfigPath: "/a"}}
	got := mergeToolConfigs(existing, incoming)
	assert.Len(t, got, 2)
	assert.Equal(t, "cursor", got[0].Tool)
	assert.Equal(t, "claude-code", got[1].Tool)
}

func TestMergeToolConfigs_ReplacesExisting(t *testing.T) {
	t.Parallel()
	existing := []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/old"}}
	incoming := []ToolConfig{{Tool: "cursor", Mode: "proxy", ConfigPath: "/new"}}
	got := mergeToolConfigs(existing, incoming)
	assert.Len(t, got, 1)
	assert.Equal(t, "/new", got[0].ConfigPath)
}

func TestMergeToolConfigs_MixedReplaceAndAppend(t *testing.T) {
	t.Parallel()
	existing := []ToolConfig{
		{Tool: "cursor", ConfigPath: "/old-cursor"},
		{Tool: "vscode", ConfigPath: "/old-vscode"},
	}
	incoming := []ToolConfig{
		{Tool: "cursor", ConfigPath: "/new-cursor"},
		{Tool: "claude-code", ConfigPath: "/claude"},
	}
	got := mergeToolConfigs(existing, incoming)
	assert.Len(t, got, 3)
	assert.Equal(t, "/new-cursor", got[0].ConfigPath)
	assert.Equal(t, "/old-vscode", got[1].ConfigPath)
	assert.Equal(t, "/claude", got[2].ConfigPath)
}

func TestMergeToolConfigs_DuplicatesInIncoming(t *testing.T) {
	t.Parallel()
	// If incoming contains the same tool name twice, the last entry wins and
	// the result must not contain duplicates.
	incoming := []ToolConfig{
		{Tool: "claude-code", ConfigPath: "/first"},
		{Tool: "claude-code", ConfigPath: "/second"},
	}
	got := mergeToolConfigs(nil, incoming)
	assert.Len(t, got, 1)
	assert.Equal(t, "/second", got[0].ConfigPath)
}

// ── isTarget ─────────────────────────────────────────────────────────────────

func TestIsTarget(t *testing.T) {
	t.Parallel()
	targets := []ToolConfig{
		{Tool: "claude-code"},
		{Tool: "cursor"},
	}
	assert.True(t, isTarget(targets, "claude-code"))
	assert.True(t, isTarget(targets, "cursor"))
	assert.False(t, isTarget(targets, "vscode"))
	assert.False(t, isTarget(targets, ""))
}

// ── Teardown purgeTokens path ─────────────────────────────────────────────────

// stubGatewayManager is a minimal GatewayManager for Teardown tests.
type stubGatewayManager struct {
	reverted []string
}

func (*stubGatewayManager) DetectedLLMGatewayClients() []string { return nil }
func (*stubGatewayManager) ConfigureLLMGateway(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*stubGatewayManager) LLMGatewayModeFor(_ string) string { return "" }
func (*stubGatewayManager) LLMSetupNoteFor(_ string) string   { return "" }
func (s *stubGatewayManager) RevertLLMGateway(clientType, _ string) error {
	s.reverted = append(s.reverted, clientType)
	return nil
}

// stubConfigUpdater is a minimal ConfigUpdater for Teardown tests.
type stubConfigUpdater struct {
	cfg Config
}

func (s *stubConfigUpdater) GetLLMConfig() Config { return s.cfg }
func (s *stubConfigUpdater) UpdateLLMConfig(fn func(*Config) error) error {
	return fn(&s.cfg)
}

func TestTeardown_PurgeTokens_ClearsConfigRefsAndDeletesSecrets(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sp := secretsmocks.NewMockProvider(ctrl)
	sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
		CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
	}).AnyTimes()
	// DeleteCachedTokens lists then deletes; for simplicity return empty list so
	// the delete call is skipped — we only need to verify the config refs are cleared.
	sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, nil)

	expiry := time.Now()
	provider := &stubConfigUpdater{cfg: Config{
		OIDC: OIDCConfig{
			CachedRefreshTokenRef: "some-ref",
			CachedTokenExpiry:     expiry,
		},
		ConfiguredTools: []ToolConfig{{Tool: "cursor", ConfigPath: "/tmp/cursor.json"}},
	}}
	gm := &stubGatewayManager{}

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr, gm, "", true, provider, sp)
	require.NoError(t, err)

	// Config refs must be cleared.
	assert.Empty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	assert.True(t, provider.cfg.OIDC.CachedTokenExpiry.IsZero())
	// Tool must have been reverted.
	assert.Equal(t, []string{"cursor"}, gm.reverted)
}

func TestTeardown_NoPurge_LeavesTokenRefsIntact(t *testing.T) {
	t.Parallel()

	expiry := time.Now()
	provider := &stubConfigUpdater{cfg: Config{
		OIDC: OIDCConfig{
			CachedRefreshTokenRef: "some-ref",
			CachedTokenExpiry:     expiry,
		},
		ConfiguredTools: []ToolConfig{{Tool: "cursor", ConfigPath: "/tmp/cursor.json"}},
	}}
	gm := &stubGatewayManager{}

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr, gm, "", false, provider, nil)
	require.NoError(t, err)

	// Token refs must be untouched when purgeTokens=false.
	assert.Equal(t, "some-ref", provider.cfg.OIDC.CachedRefreshTokenRef)
	assert.Equal(t, expiry, provider.cfg.OIDC.CachedTokenExpiry)
}

// ── AnthropicPathPrefix / configureDetectedTools ──────────────────────────────

// capturingGatewayManager records the ApplyConfig passed to ConfigureLLMGateway.
type capturingGatewayManager struct {
	mode    string // returned by LLMGatewayModeFor
	applied []llmgateway.ApplyConfig
}

func (*capturingGatewayManager) DetectedLLMGatewayClients() []string { return nil }
func (g *capturingGatewayManager) ConfigureLLMGateway(_ string, cfg llmgateway.ApplyConfig) (string, error) {
	g.applied = append(g.applied, cfg)
	return "/path/to/settings.json", nil
}
func (g *capturingGatewayManager) LLMGatewayModeFor(_ string) string { return g.mode }
func (*capturingGatewayManager) LLMSetupNoteFor(_ string) string     { return "" }
func (*capturingGatewayManager) RevertLLMGateway(_, _ string) error  { return nil }

func TestConfigureDetectedTools_PathPrefixAppendedForDirectMode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "direct"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedTools(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1", `"thv" llm token`,
		false, "/anthropic",
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	// The Anthropic base URL must be gateway + prefix, not just the gateway.
	assert.Equal(t, "https://gw.example.com/anthropic", gm.applied[0].AnthropicBaseURL)
	assert.Equal(t, "https://gw.example.com", gm.applied[0].GatewayURL)
}

func TestConfigureDetectedTools_NoPrefixWhenEmpty(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "direct"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedTools(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1", `"thv" llm token`,
		false, "", // no prefix
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	// AnthropicBaseURL must be empty so llmValueForSpec falls back to GatewayURL.
	assert.Empty(t, gm.applied[0].AnthropicBaseURL)
}

func TestConfigureDetectedTools_PrefixNotAppliedForProxyMode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "proxy"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedTools(
		&out, &errOut, gm,
		[]string{"cursor"},
		"https://gw.example.com", "http://localhost:14000/v1", `"thv" llm token`,
		false, "/anthropic",
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	// Proxy-mode tools must never receive an AnthropicBaseURL.
	assert.Empty(t, gm.applied[0].AnthropicBaseURL)
}

// ── probeAnthropicPrefix ──────────────────────────────────────────────────────

func TestProbeAnthropicPrefix_Returns_Anthropic_On_401(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	prefix := probeAnthropicPrefix(context.Background(), srv.URL, false)
	assert.Equal(t, "/anthropic", prefix)
}

func TestProbeAnthropicPrefix_Returns_Empty_On_404(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	prefix := probeAnthropicPrefix(context.Background(), srv.URL, false)
	assert.Empty(t, prefix)
}

func TestProbeAnthropicPrefix_Returns_Empty_On_NetworkError(t *testing.T) {
	t.Parallel()

	// Use a URL that will immediately refuse connections.
	prefix := probeAnthropicPrefix(context.Background(), "http://127.0.0.1:1", false)
	assert.Empty(t, prefix)
}

func TestProbeAnthropicPrefix_Returns_Empty_For_EmptyGatewayURL(t *testing.T) {
	t.Parallel()

	prefix := probeAnthropicPrefix(context.Background(), "", false)
	assert.Empty(t, prefix)
}
