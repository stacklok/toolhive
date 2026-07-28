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

// ── resolveBedrockModels ──────────────────────────────────────────────────────

func TestResolveBedrockModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		models       []string
		enable1M     bool
		wantHaiku    string
		wantOpus     string
		wantSonnet   string
		wantWarnings []string
	}{
		{
			name:       "defaults when no overrides",
			wantHaiku:  defaultBedrockHaikuModel,
			wantOpus:   defaultBedrockOpusModel,
			wantSonnet: defaultBedrockSonnetModel,
		},
		{
			name:       "override each tier by substring",
			models:     []string{"us.anthropic.claude-haiku-x", "us.anthropic.claude-opus-x", "us.anthropic.claude-sonnet-x"},
			wantHaiku:  "us.anthropic.claude-haiku-x",
			wantOpus:   "us.anthropic.claude-opus-x",
			wantSonnet: "us.anthropic.claude-sonnet-x",
		},
		{
			name:       "enable1M appends [1m] to opus and sonnet only",
			enable1M:   true,
			wantHaiku:  defaultBedrockHaikuModel,
			wantOpus:   defaultBedrockOpusModel + "[1m]",
			wantSonnet: defaultBedrockSonnetModel + "[1m]",
		},
		{
			name:         "unmatched entry is reported and ignored",
			models:       []string{"us.anthropic.claude-opus-x", "some-random-model"},
			wantHaiku:    defaultBedrockHaikuModel,
			wantOpus:     "us.anthropic.claude-opus-x",
			wantSonnet:   defaultBedrockSonnetModel,
			wantWarnings: []string{`--models entry "some-random-model" did not match a tier (expected haiku/opus/sonnet in the ID); ignored`},
		},
		{
			name:       "matching is case-insensitive",
			models:     []string{"US.ANTHROPIC.CLAUDE-OPUS-X"},
			wantHaiku:  defaultBedrockHaikuModel,
			wantOpus:   "US.ANTHROPIC.CLAUDE-OPUS-X",
			wantSonnet: defaultBedrockSonnetModel,
		},
		{
			name:       "duplicate-tier override reports the clobbered entry and last wins",
			models:     []string{"us.anthropic.claude-sonnet-5", "us.anthropic.claude-sonnet-4-6"},
			wantHaiku:  defaultBedrockHaikuModel,
			wantOpus:   defaultBedrockOpusModel,
			wantSonnet: "us.anthropic.claude-sonnet-4-6",
			wantWarnings: []string{
				`--models entry "us.anthropic.claude-sonnet-4-6" overrides "us.anthropic.claude-sonnet-5" for the sonnet tier; only the last is used`,
			},
		},
		{
			name:       "already-suffixed override is not doubled with enable1M",
			models:     []string{"us.anthropic.claude-opus-4-8[1m]"},
			enable1M:   true,
			wantHaiku:  defaultBedrockHaikuModel,
			wantOpus:   "us.anthropic.claude-opus-4-8[1m]",
			wantSonnet: defaultBedrockSonnetModel + "[1m]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			haiku, opus, sonnet, warnings := resolveBedrockModels(tt.models, tt.enable1M)
			assert.Equal(t, tt.wantHaiku, haiku)
			assert.Equal(t, tt.wantOpus, opus)
			assert.Equal(t, tt.wantSonnet, sonnet)
			assert.Equal(t, tt.wantWarnings, warnings)
		})
	}
}

// ── warnBedrockNoEffect ───────────────────────────────────────────────────────

func TestWarnBedrockNoEffect(t *testing.T) {
	t.Parallel()

	claudeCode := []ToolConfig{{Tool: claudeCodeClient}}
	cursorOnly := []ToolConfig{{Tool: "cursor"}}

	tests := []struct {
		name            string
		opts            SetOptions // flags passed THIS run
		effectiveCompat bool       // merged persisted + inline compat
		configured      []ToolConfig
		wantWarn        string // substring expected on stderr; "" means no output
	}{
		{
			name:            "compat passed this run with claude-code configured is silent",
			opts:            SetOptions{BedrockCompat: boolPtr(true)},
			effectiveCompat: true,
			configured:      claudeCode,
		},
		{
			name:            "compat passed this run without claude-code warns",
			opts:            SetOptions{BedrockCompat: boolPtr(true)},
			effectiveCompat: true,
			configured:      cursorOnly,
			wantWarn:        "--bedrock-compat was set but Claude Code was not configured",
		},
		{
			// Regression: bedrock-compat is persisted, so a later run that omits the
			// flag must NOT warn even though effective compat is on and claude-code
			// is not among the configured tools.
			name:            "persisted compat, flag not passed this run, is silent",
			opts:            SetOptions{},
			effectiveCompat: true,
			configured:      cursorOnly,
		},
		{
			name:       "enable1M passed this run without compat warns",
			opts:       SetOptions{Enable1M: boolPtr(true)},
			configured: claudeCode,
			wantWarn:   "--enable-1m has no effect without --bedrock-compat",
		},
		{
			name:            "enable1M passed this run with effective compat is silent",
			opts:            SetOptions{Enable1M: boolPtr(true)},
			effectiveCompat: true,
			configured:      claudeCode,
		},
		{
			name:       "no bedrock flags this run is silent",
			opts:       SetOptions{},
			configured: claudeCode,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var errOut bytes.Buffer
			warnBedrockNoEffect(&errOut, tt.opts, tt.effectiveCompat, tt.configured)
			if tt.wantWarn == "" {
				assert.Empty(t, errOut.String())
				return
			}
			assert.Contains(t, errOut.String(), tt.wantWarn)
		})
	}
}

// TestConfigureDetectedTools_BedrockClaudeCode verifies that bedrock-compat
// populates the ApplyConfig bedrock fields for claude-code (with defaults) and
// leaves them untouched for a non-claude-code client.
func TestConfigureDetectedTools_BedrockClaudeCode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "direct"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedTools(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1", `"thv" llm token`,
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "/anthropic", nil,
		BedrockConfig{Compat: true, Enable1M: true},
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	got := gm.applied[0]
	assert.True(t, got.BedrockCompat)
	assert.Equal(t, defaultBedrockHaikuModel, got.BedrockHaikuModel)
	assert.Equal(t, defaultBedrockOpusModel+"[1m]", got.BedrockOpusModel)
	assert.Equal(t, defaultBedrockSonnetModel+"[1m]", got.BedrockSonnetModel)
}

func TestConfigureDetectedTools_BedrockSkippedForNonClaudeCode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "proxy"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedTools(
		&out, &errOut, gm,
		[]string{"cursor"},
		"https://gw.example.com", "http://localhost:14000/v1", `"thv" llm token`,
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "", nil,
		BedrockConfig{Compat: true},
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)
	assert.False(t, gm.applied[0].BedrockCompat)
	assert.Empty(t, gm.applied[0].BedrockOpusModel)
}

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
func (*stubGatewayManager) IsManaged(_ string) bool           { return false }
func (*stubGatewayManager) ConfigureEnvFile(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*stubGatewayManager) RevertEnvFile(_, _ string) error { return nil }
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

// ── Setup --lazy path ─────────────────────────────────────────────────────────

// setupGatewayManager reports one or more detected clients and patches them
// successfully, for Setup-level tests. mode is returned by LLMGatewayModeFor;
// use "proxy" to avoid the direct-mode Anthropic-prefix probe.
type setupGatewayManager struct {
	detected []string
	mode     string
}

func (g *setupGatewayManager) DetectedLLMGatewayClients() []string { return g.detected }
func (*setupGatewayManager) ConfigureLLMGateway(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "/tmp/settings.json", nil
}
func (g *setupGatewayManager) LLMGatewayModeFor(_ string) string { return g.mode }
func (*setupGatewayManager) IsManaged(_ string) bool             { return false }
func (*setupGatewayManager) ConfigureEnvFile(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*setupGatewayManager) RevertEnvFile(_, _ string) error    { return nil }
func (*setupGatewayManager) RevertLLMGateway(_, _ string) error { return nil }

// configuredSetupProvider returns a ConfigUpdater whose config satisfies
// IsConfigured()/Validate() so Setup proceeds past its configuration checks.
func configuredSetupProvider() *stubConfigUpdater {
	return &stubConfigUpdater{cfg: Config{
		GatewayURL: "https://llm.example.com",
		OIDC: OIDCConfig{
			Issuer:   "https://auth.example.com",
			ClientID: "test-client",
		},
	}}
}

func TestSetup_Lazy_SkipsLoginAndPersistsTools(t *testing.T) {
	t.Parallel()

	gm := &setupGatewayManager{detected: []string{"cursor"}, mode: "proxy"}
	provider := configuredSetupProvider()

	loginCalled := false
	login := func(_ context.Context, _ *Config) error {
		loginCalled = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	// anthropicPathPrefixSet=true skips the network probe; lazy=true.
	err := Setup(
		context.Background(), &stdout, &stderr, gm, provider, login,
		SetOptions{}, "", true, "", true,
	)
	require.NoError(t, err)

	assert.False(t, loginCalled, "lazy mode must not invoke the OIDC login")
	// Tool config must still be persisted even though login was skipped.
	require.Len(t, provider.cfg.ConfiguredTools, 1)
	assert.Equal(t, "cursor", provider.cfg.ConfiguredTools[0].Tool)
	// User must be told that login is deferred to the first request.
	assert.Contains(t, stdout.String(), "Lazy mode")
	assert.Contains(t, stdout.String(), "first")
}

func TestSetup_NonLazy_InvokesLogin(t *testing.T) {
	t.Parallel()

	gm := &setupGatewayManager{detected: []string{"cursor"}, mode: "proxy"}
	provider := configuredSetupProvider()

	loginCalled := false
	login := func(_ context.Context, _ *Config) error {
		loginCalled = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	err := Setup(
		context.Background(), &stdout, &stderr, gm, provider, login,
		SetOptions{}, "", true, "", false,
	)
	require.NoError(t, err)

	assert.True(t, loginCalled, "non-lazy mode must invoke the OIDC login")
	require.Len(t, provider.cfg.ConfiguredTools, 1)
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
func (*capturingGatewayManager) IsManaged(_ string) bool             { return false }
func (*capturingGatewayManager) LLMSetupNoteFor(_ string) string     { return "" }
func (*capturingGatewayManager) RevertLLMGateway(_, _ string) error  { return nil }
func (*capturingGatewayManager) ConfigureEnvFile(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*capturingGatewayManager) RevertEnvFile(_, _ string) error { return nil }

func TestConfigureDetectedTools_PathPrefixAppendedForDirectMode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "direct"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedTools(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1", `"thv" llm token`,
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "/anthropic", nil,
		BedrockConfig{},
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
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "", nil, // no prefix
		BedrockConfig{},
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
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "/anthropic", nil,
		BedrockConfig{},
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	// Proxy-mode tools must never receive an AnthropicBaseURL.
	assert.Empty(t, gm.applied[0].AnthropicBaseURL)
}

func TestTokenHelperCommandNeeded(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		modes    map[string]string
		detected []string
		want     bool
	}{
		{
			name:     "codex-only run never needs the shell-string helper",
			modes:    map[string]string{"codex": llmgateway.ModeCodexAuth},
			detected: []string{"codex"},
			want:     false,
		},
		{
			name:     "proxy-only run never needs the shell-string helper",
			modes:    map[string]string{"cursor": llmgateway.ModeProxy},
			detected: []string{"cursor"},
			want:     false,
		},
		{
			name:     "direct mode needs it",
			modes:    map[string]string{"claude-code": llmgateway.ModeDirect},
			detected: []string{"claude-code"},
			want:     true,
		},
		{
			name:     "credential-helper mode needs it",
			modes:    map[string]string{"claude-desktop": llmgateway.ModeCredentialHelper},
			detected: []string{"claude-desktop"},
			want:     true,
		},
		{
			name: "any detected tool needing it is enough",
			modes: map[string]string{
				"codex":       llmgateway.ModeCodexAuth,
				"claude-code": llmgateway.ModeDirect,
			},
			detected: []string{"codex", "claude-code"},
			want:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &modeLookupGatewayManager{modes: tt.modes}
			assert.Equal(t, tt.want, tokenHelperCommandNeeded(gm, tt.detected))
		})
	}
}

// modeLookupGatewayManager is a minimal GatewayManager whose LLMGatewayModeFor
// returns a per-client mode from a fixed map, for tokenHelperCommandNeeded tests.
type modeLookupGatewayManager struct{ modes map[string]string }

func (*modeLookupGatewayManager) DetectedLLMGatewayClients() []string { return nil }
func (*modeLookupGatewayManager) ConfigureLLMGateway(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (g *modeLookupGatewayManager) LLMGatewayModeFor(c string) string { return g.modes[c] }
func (*modeLookupGatewayManager) IsManaged(_ string) bool             { return false }
func (*modeLookupGatewayManager) ConfigureEnvFile(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*modeLookupGatewayManager) RevertEnvFile(_, _ string) error    { return nil }
func (*modeLookupGatewayManager) RevertLLMGateway(_, _ string) error { return nil }

func TestBuildTokenHelperArgv(t *testing.T) {
	t.Parallel()

	path, args, err := buildTokenHelperArgv()
	require.NoError(t, err)
	assert.NotEmpty(t, path)
	assert.Equal(t, []string{"llm", "token", "--skip-browser"}, args)
}

func TestWarnTLSSkipVerify_CodexWarning(t *testing.T) {
	t.Parallel()

	var errOut bytes.Buffer
	warnTLSSkipVerify(&errOut, true, []ToolConfig{{Tool: "codex", Mode: llmgateway.ModeCodexAuth}})
	out := errOut.String()
	assert.Contains(t, out, "Warning:")
	assert.Contains(t, out, "was NOT applied to codex")
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

// managedGatewayManager is a stub whose IsManaged is configurable per client,
// for exercising warnCredentialHelperTools' managed-profile warning branch.
type managedGatewayManager struct{ managed map[string]bool }

func (*managedGatewayManager) DetectedLLMGatewayClients() []string { return nil }
func (*managedGatewayManager) ConfigureLLMGateway(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*managedGatewayManager) LLMGatewayModeFor(_ string) string {
	return llmgateway.ModeCredentialHelper
}
func (g *managedGatewayManager) IsManaged(c string) bool { return g.managed[c] }
func (*managedGatewayManager) ConfigureEnvFile(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*managedGatewayManager) RevertEnvFile(_, _ string) error    { return nil }
func (*managedGatewayManager) RevertLLMGateway(_, _ string) error { return nil }

func TestWarnCredentialHelperTools(t *testing.T) {
	t.Parallel()
	gm := &managedGatewayManager{managed: map[string]bool{"claude-desktop": true}}
	var out, errOut bytes.Buffer

	warnCredentialHelperTools(&out, &errOut, gm, []ToolConfig{
		{Tool: "claude-desktop", Mode: llmgateway.ModeCredentialHelper},
		{Tool: "other-desktop", Mode: llmgateway.ModeCredentialHelper},
		{Tool: "claude-code", Mode: "direct"},
	})

	// The relaunch note prints on stdout for every credential-helper tool, and
	// not for non-credential-helper tools.
	assert.Contains(t, out.String(), "claude-desktop reads its configuration only at launch")
	assert.Contains(t, out.String(), "other-desktop reads its configuration only at launch")
	assert.NotContains(t, out.String(), "claude-code")

	// The MDM warning prints on stderr only for the managed tool.
	assert.Contains(t, errOut.String(), "managed-preferences profile for claude-desktop")
	assert.NotContains(t, errOut.String(), "profile for other-desktop")
}
