// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/llmgateway"
	"github.com/stacklok/toolhive/pkg/networking"
	"github.com/stacklok/toolhive/pkg/secrets"
	secretsmocks "github.com/stacklok/toolhive/pkg/secrets/mocks"
)

// ── resolveBedrockModels ──────────────────────────────────────────────────────

func TestResolveBedrockModels(t *testing.T) {
	t.Parallel()

	// stagingCatalog mirrors a gateway that routes both Anthropic-direct and
	// Bedrock models, with several generations per family.
	stagingCatalog := []string{
		"claude-fable-5-1", "claude-haiku-5-5", "claude-opus-5-5", "claude-sonnet-5-5",
		"us.anthropic.claude-fable-5-1",
		"us.anthropic.claude-haiku-4-5-20251001-v1:0",
		"us.anthropic.claude-opus-4-8",
		"us.anthropic.claude-opus-4-6-v1",
		"us.anthropic.claude-sonnet-5",
		"us.anthropic.claude-sonnet-4-6",
	}

	tests := []struct {
		name         string
		models       []string
		discovered   []string
		enable1M     bool
		want         bedrockTiers
		wantWarnings []string
	}{
		{
			name: "no pins without overrides or discovery",
		},
		{
			name:       "newest Bedrock model per family, ignoring Anthropic-form IDs",
			discovered: stagingCatalog,
			want: bedrockTiers{
				haiku:  "us.anthropic.claude-haiku-4-5-20251001-v1:0",
				opus:   "us.anthropic.claude-opus-4-8",
				sonnet: "us.anthropic.claude-sonnet-5",
				fable:  "us.anthropic.claude-fable-5-1",
			},
		},
		{
			name: "minor version and release date order within a family",
			discovered: []string{
				"anthropic.claude-sonnet-4-20250514-v1:0",
				"global.anthropic.claude-sonnet-4-5-20250929-v1:0",
				"eu.anthropic.claude-sonnet-4-5-20250101-v1:0",
			},
			want: bedrockTiers{sonnet: "global.anthropic.claude-sonnet-4-5-20250929-v1:0"},
		},
		{
			name: "version-first legacy IDs rank within a family",
			discovered: []string{
				"anthropic.claude-3-haiku-20240307-v1:0",
				"anthropic.claude-3-5-haiku-20241022-v1:0",
			},
			want: bedrockTiers{haiku: "anthropic.claude-3-5-haiku-20241022-v1:0"},
		},
		{
			name: "modern family-first outranks legacy version-first",
			discovered: []string{
				"anthropic.claude-3-5-haiku-20241022-v1:0",
				"us.anthropic.claude-haiku-4-5-20251001-v1:0",
			},
			want: bedrockTiers{haiku: "us.anthropic.claude-haiku-4-5-20251001-v1:0"},
		},
		{
			name:       "equal versions tie-break on the smallest ID",
			discovered: []string{"us.anthropic.claude-opus-4-8", "eu.anthropic.claude-opus-4-8"},
			want:       bedrockTiers{opus: "eu.anthropic.claude-opus-4-8"},
		},
		{
			name:       "override replaces only its own discovered tier",
			models:     []string{"us.anthropic.claude-opus-4-6-v1"},
			discovered: stagingCatalog,
			want: bedrockTiers{
				haiku:  "us.anthropic.claude-haiku-4-5-20251001-v1:0",
				opus:   "us.anthropic.claude-opus-4-6-v1",
				sonnet: "us.anthropic.claude-sonnet-5",
				fable:  "us.anthropic.claude-fable-5-1",
			},
		},
		{
			name:   "override each tier by substring",
			models: []string{"x-haiku", "x-opus", "x-sonnet", "x-fable"},
			want:   bedrockTiers{haiku: "x-haiku", opus: "x-opus", sonnet: "x-sonnet", fable: "x-fable"},
		},
		{
			name:       "enable1M appends [1m] to opus and sonnet only",
			discovered: stagingCatalog,
			enable1M:   true,
			want: bedrockTiers{
				haiku:  "us.anthropic.claude-haiku-4-5-20251001-v1:0",
				opus:   "us.anthropic.claude-opus-4-8[1m]",
				sonnet: "us.anthropic.claude-sonnet-5[1m]",
				fable:  "us.anthropic.claude-fable-5-1",
			},
		},
		{
			name:     "enable1M leaves unpinned tiers empty",
			enable1M: true,
		},
		{
			name:         "unmatched entry is reported and ignored",
			models:       []string{"us.anthropic.claude-opus-x", "some-random-model"},
			want:         bedrockTiers{opus: "us.anthropic.claude-opus-x"},
			wantWarnings: []string{`--models entry "some-random-model" did not match a tier (expected haiku/opus/sonnet/fable in the ID); ignored`},
		},
		{
			name:   "matching is case-insensitive",
			models: []string{"US.ANTHROPIC.CLAUDE-OPUS-X"},
			want:   bedrockTiers{opus: "US.ANTHROPIC.CLAUDE-OPUS-X"},
		},
		{
			name:   "duplicate-tier override reports the clobbered entry and last wins",
			models: []string{"us.anthropic.claude-sonnet-5", "us.anthropic.claude-sonnet-4-6"},
			want:   bedrockTiers{sonnet: "us.anthropic.claude-sonnet-4-6"},
			wantWarnings: []string{
				`--models entry "us.anthropic.claude-sonnet-4-6" overrides "us.anthropic.claude-sonnet-5" for the sonnet tier; only the last is used`,
			},
		},
		{
			name:     "already-suffixed override is not doubled with enable1M",
			models:   []string{"us.anthropic.claude-opus-4-8[1m]"},
			enable1M: true,
			want:     bedrockTiers{opus: "us.anthropic.claude-opus-4-8[1m]"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warnings := resolveBedrockModels(tt.models, tt.discovered, tt.enable1M)
			assert.Equal(t, tt.want, got)
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
// populates the ApplyConfig bedrock fields for claude-code (pinning only the
// tiers named by --models) and leaves them untouched for a non-claude-code client.
func TestConfigureDetectedTools_BedrockClaudeCode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "direct"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedToolsWithDiscovery(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1",
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "/anthropic", []string{"us.anthropic.claude-opus-x"}, discoveredGatewayModels{}, false,
		BedrockConfig{Compat: true, Enable1M: true},
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	got := gm.applied[0]
	assert.True(t, got.BedrockCompat)
	assert.Empty(t, got.BedrockHaikuModel)
	assert.Equal(t, "us.anthropic.claude-opus-x[1m]", got.BedrockOpusModel)
	assert.Empty(t, got.BedrockSonnetModel)
	// Only opus was pinned (via --models) and nothing was discovered, so the
	// unpinned tiers are reported in canonical order.
	assert.Contains(t, errOut.String(),
		"no Bedrock model found in the gateway catalogue for the haiku, sonnet, fable tier(s)")
}

func TestConfigureDetectedTools_BedrockSkippedForNonClaudeCode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "proxy"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedToolsWithDiscovery(
		&out, &errOut, gm,
		[]string{"cursor"},
		"https://gw.example.com", "http://localhost:14000/v1",
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "", nil, discoveredGatewayModels{}, false,
		BedrockConfig{Compat: true},
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)
	assert.False(t, gm.applied[0].BedrockCompat)
	assert.Empty(t, gm.applied[0].BedrockOpusModel)
}

func TestConfigureDetectedTools_PromptCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		clientType       string
		mode             string
		shortPromptCache bool
		conflict         string
		conflictErr      error
		wantStderr       []string
		wantStderrAbsent []string
		wantConflictCall bool
	}{
		{
			name:             "Claude Code gets one-hour lifetime by default",
			clientType:       claudeCodeClient,
			mode:             llmgateway.ModeDirect,
			wantConflictCall: true,
		},
		{
			name:             "non-Claude client has no prompt-cache reporting",
			clientType:       "cursor",
			mode:             llmgateway.ModeProxy,
			wantStderrAbsent: []string{"Warning:"},
		},
		{
			name:             "short-cache opt-out skips conflict inspection",
			clientType:       claudeCodeClient,
			mode:             llmgateway.ModeDirect,
			shortPromptCache: true,
			wantStderrAbsent: []string{"Warning:"},
		},
		{
			name:             "five-minute override warns but one-hour settings are still written",
			clientType:       claudeCodeClient,
			mode:             llmgateway.ModeDirect,
			conflict:         "CLAUDE_CODE_PROMPT_CACHE_TTL=5m in the process environment",
			wantStderr:       []string{"CLAUDE_CODE_PROMPT_CACHE_TTL=5m", "wrote the one-hour", promptCacheVerificationCommand},
			wantConflictCall: true,
		},
		{
			name:             "inspection failure warns but one-hour settings are still written",
			clientType:       claudeCodeClient,
			mode:             llmgateway.ModeDirect,
			conflictErr:      errors.New("invalid settings"),
			wantStderr:       []string{"could not inspect claude-code", "wrote the one-hour settings anyway"},
			wantConflictCall: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &capturingGatewayManager{
				mode:             tt.mode,
				cacheConflict:    tt.conflict,
				cacheConflictErr: tt.conflictErr,
			}
			var out, errOut bytes.Buffer

			configured, err := configureDetectedToolsWithDiscovery(
				&out, &errOut, gm, []string{tt.clientType},
				"https://gw.example.com", "http://localhost:14000/v1",
				"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
				false, "", nil, discoveredGatewayModels{}, tt.shortPromptCache, BedrockConfig{},
			)
			require.NoError(t, err)
			require.Len(t, configured, 1)
			require.Len(t, gm.applied, 1)
			assert.Equal(t, tt.shortPromptCache, gm.applied[0].ShortPromptCache)
			assert.Equal(t, tt.wantConflictCall, gm.conflictCalls == 1)
			for _, want := range tt.wantStderr {
				assert.Contains(t, errOut.String(), want)
			}
			for _, absent := range tt.wantStderrAbsent {
				assert.NotContains(t, errOut.String(), absent)
			}
		})
	}
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
func (*stubGatewayManager) PromptCacheConflict(_ string) (string, error) {
	return "", nil
}
func (*stubGatewayManager) IsManaged(_ string) bool                { return false }
func (*stubGatewayManager) LLMClientDetectionHint(_ string) string { return "" }
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
	cfg         Config
	updateCalls int
	updateErr   error
}

func (s *stubConfigUpdater) GetLLMConfig() Config { return s.cfg }
func (s *stubConfigUpdater) UpdateLLMConfig(fn func(*Config) error) error {
	s.updateCalls++
	if s.updateErr != nil {
		return s.updateErr
	}
	return fn(&s.cfg)
}

func TestTeardown_PurgeTokens_ClearsConfigRefsAndDeletesSecretsDuringPartialTeardown(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sp := secretsmocks.NewMockProvider(ctrl)
	sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
		CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
	}).AnyTimes()
	refreshKey := secrets.SystemKeyPrefix + string(secrets.ScopeLLM) + "_refresh_token"
	accessKey := secrets.SystemKeyPrefix + string(secrets.ScopeLLM) + "_access_token"
	sp.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
		{Key: refreshKey},
		{Key: accessKey},
	}, nil)
	sp.EXPECT().DeleteSecrets(gomock.Any(), gomock.InAnyOrder([]string{
		refreshKey,
		accessKey,
	})).Return(nil)

	expiry := time.Now()
	provider := &stubConfigUpdater{cfg: Config{
		OIDC: OIDCConfig{
			CachedRefreshTokenRef: "some-ref",
			CachedTokenExpiry:     expiry,
		},
		ConfiguredTools: []ToolConfig{
			{Tool: "cursor", ConfigPath: "/tmp/cursor.json"},
			{Tool: "claude-code", ConfigPath: "/tmp/claude.json"},
		},
	}}
	gm := &stubGatewayManager{}

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr, gm, "cursor", false, true, provider,
		func() (secrets.Provider, error) { return sp, nil })
	require.NoError(t, err)

	// Config refs must be cleared.
	assert.Empty(t, provider.cfg.OIDC.CachedRefreshTokenRef)
	assert.True(t, provider.cfg.OIDC.CachedTokenExpiry.IsZero())
	require.Len(t, provider.cfg.ConfiguredTools, 1)
	assert.Equal(t, "claude-code", provider.cfg.ConfiguredTools[0].Tool)
	// Only the targeted tool must have been reverted.
	assert.Equal(t, []string{"cursor"}, gm.reverted)
	assert.Contains(t, stdout.String(), "Remaining configured tools share this session")
	assert.Contains(t, stdout.String(), "active process may recreate cached credentials")
}

// fullSetupConfig returns a config as "thv llm setup" would leave it, with the
// given tools configured and every persisted setting populated.
func fullSetupConfig(tools ...string) Config {
	configured := make([]ToolConfig, len(tools))
	for i, tool := range tools {
		configured[i] = ToolConfig{Tool: tool, ConfigPath: "/tmp/" + tool + ".json"}
	}
	return Config{
		GatewayURL:    "https://gw.example.com",
		TLSSkipVerify: true,
		OIDC: OIDCConfig{
			Issuer:                "https://auth.example.com",
			ClientID:              "cid",
			CachedRefreshTokenRef: "secret-ref",
		},
		Proxy:           ProxyConfig{ListenPort: 14001},
		Bedrock:         BedrockConfig{Compat: true, Enable1M: true},
		Models:          []string{"us.anthropic.claude-opus-4-8"},
		ConfiguredTools: configured,
	}
}

// TestTeardown_ResetsConfigWhenLastToolReverted verifies that reverting the last
// configured tool resets the whole LLM config. Settings like Bedrock compat are
// deliberately sticky across "thv llm setup" re-runs, so leaving them behind
// would let a later setup silently re-apply settings the user just tore down.
//
// Cached token state is retained only when the user explicitly requests it.
func TestTeardown_KeepTokensResetsConfigWhenLastToolReverted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured []string
		targetTool string
	}{
		{
			name:       "targeted teardown of the only tool",
			configured: []string{"claude-code"},
			targetTool: "claude-code",
		},
		{
			name:       "untargeted teardown of every tool",
			configured: []string{"claude-code", "claude-desktop", "cursor"},
			targetTool: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := &stubConfigUpdater{cfg: fullSetupConfig(tt.configured...)}

			var stdout, stderr bytes.Buffer
			err := Teardown(context.Background(), &stdout, &stderr,
				&stubGatewayManager{}, tt.targetTool, true, false, provider, nil)
			require.NoError(t, err)

			want := Config{OIDC: OIDCConfig{CachedRefreshTokenRef: "secret-ref"}}
			assert.Equal(t, want, provider.cfg,
				"no tools remain, so every persisted setting except cached token state must be reset")
			assert.Contains(t, stdout.String(), "Cleared the LLM gateway configuration")
		})
	}
}

// TestTeardown_DefaultPurgeWhenNoToolsRemain verifies that both forms of full
// teardown delete cached tokens without requiring --purge-tokens.
func TestTeardown_DefaultPurgeWhenNoToolsRemain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		configured []string
		targetTool string
	}{
		{
			name:       "targeted teardown removes final tool",
			configured: []string{"claude-code"},
			targetTool: "claude-code",
		},
		{
			name:       "untargeted teardown removes all tools",
			configured: []string{"claude-code", "cursor"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			sp := secretsmocks.NewMockProvider(ctrl)
			sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
				CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
			}).AnyTimes()
			sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, nil)

			factoryCalls := 0
			provider := &stubConfigUpdater{cfg: fullSetupConfig(tt.configured...)}
			var stdout, stderr bytes.Buffer
			err := Teardown(context.Background(), &stdout, &stderr,
				&stubGatewayManager{}, tt.targetTool, false, false, provider,
				func() (secrets.Provider, error) {
					factoryCalls++
					return sp, nil
				})
			require.NoError(t, err)

			assert.Equal(t, 1, factoryCalls)
			assert.Equal(t, Config{}, provider.cfg)
		})
	}
}

func TestTeardown_ZeroToolsStillPurgesCachedTokens(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sp := secretsmocks.NewMockProvider(ctrl)
	sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
		CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
	}).AnyTimes()
	sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, nil)

	factoryCalls := 0
	provider := &stubConfigUpdater{cfg: fullSetupConfig()}
	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr,
		&stubGatewayManager{}, "", false, false, provider,
		func() (secrets.Provider, error) {
			factoryCalls++
			return sp, nil
		})
	require.NoError(t, err)

	assert.Equal(t, 1, factoryCalls)
	assert.Equal(t, 1, provider.updateCalls)
	assert.Equal(t, Config{}, provider.cfg)
	assert.Contains(t, stdout.String(), "No tools are currently configured")
}

// TestTeardown_KeepsConfigWhileToolsRemain verifies that a targeted teardown
// preserves the gateway configuration the still-configured tools depend on.
// Zeroing it here would break "thv llm token" and "thv llm proxy start" for a
// tool the user never asked to touch, since both gate on IsConfigured().
func TestTeardown_KeepsConfigWhileToolsRemain(t *testing.T) {
	t.Parallel()

	provider := &stubConfigUpdater{cfg: fullSetupConfig("claude-code", "cursor")}
	factoryCalls := 0

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr,
		&stubGatewayManager{}, "claude-code", false, false, provider,
		func() (secrets.Provider, error) {
			factoryCalls++
			return nil, errors.New("must not acquire secrets provider")
		})
	require.NoError(t, err)

	assert.Zero(t, factoryCalls)
	assert.True(t, provider.cfg.IsConfigured(),
		"cursor still routes through the gateway, so its config must survive")
	assert.Equal(t, "secret-ref", provider.cfg.OIDC.CachedRefreshTokenRef,
		"an unrelated teardown must not force a fresh login")
	assert.Equal(t, 14001, provider.cfg.EffectiveProxyPort())
	assert.NotContains(t, stdout.String(), "Cleared the LLM gateway configuration")

	var remaining []string
	for _, tc := range provider.cfg.ConfiguredTools {
		remaining = append(remaining, tc.Tool)
	}
	assert.Equal(t, []string{"cursor"}, remaining)
}

func TestTeardown_KeepTokens_LeavesTokenRefsIntact(t *testing.T) {
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
	factoryCalls := 0

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr, gm, "", true, false, provider,
		func() (secrets.Provider, error) {
			factoryCalls++
			return nil, errors.New("must not acquire secrets provider")
		})
	require.NoError(t, err)

	assert.Zero(t, factoryCalls)
	// Token refs must be untouched when keepTokens=true.
	assert.Equal(t, "some-ref", provider.cfg.OIDC.CachedRefreshTokenRef)
	assert.Equal(t, expiry, provider.cfg.OIDC.CachedTokenExpiry)
}

func TestTeardown_SecretsProviderAcquisitionErrorStopsBeforeConfigUpdate(t *testing.T) {
	t.Parallel()

	original := fullSetupConfig("cursor")
	provider := &stubConfigUpdater{cfg: original}
	gm := &stubGatewayManager{}

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr, gm, "", false, false, provider,
		func() (secrets.Provider, error) { return nil, errors.New("keychain unavailable") })
	require.Error(t, err)

	assert.ErrorContains(t, err, "getting secrets provider to remove cached LLM tokens")
	assert.ErrorContains(t, err, "keychain unavailable")
	assert.Zero(t, provider.updateCalls)
	assert.Equal(t, original, provider.cfg)
	assert.Empty(t, gm.reverted)
}

func TestTeardown_ConfigUpdateFailureLeavesToolStateAfterSecretCleanup(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sp := secretsmocks.NewMockProvider(ctrl)
	sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
		CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
	}).AnyTimes()
	sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, nil)
	original := fullSetupConfig("cursor")
	provider := &stubConfigUpdater{cfg: original, updateErr: errors.New("disk full")}
	gm := &stubGatewayManager{}
	factoryCalls := 0

	var stdout, stderr bytes.Buffer
	err := Teardown(context.Background(), &stdout, &stderr, gm, "", false, false, provider,
		func() (secrets.Provider, error) {
			factoryCalls++
			return sp, nil
		})
	require.Error(t, err)

	assert.ErrorContains(t, err, "cached LLM tokens were deleted, but persisting tool configuration")
	assert.Equal(t, 1, factoryCalls)
	assert.Equal(t, 1, provider.updateCalls)
	assert.Equal(t, original, provider.cfg)
	assert.Empty(t, gm.reverted)
}

func TestTeardown_PurgeErrorsAreFatal(t *testing.T) {
	t.Parallel()

	llmRefreshKey := secrets.SystemKeyPrefix + string(secrets.ScopeLLM) + "_refresh_token"
	tests := []struct {
		name      string
		setupMock func(*secretsmocks.MockProvider)
		wantError string
	}{
		{
			name: "list error",
			setupMock: func(sp *secretsmocks.MockProvider) {
				sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, errors.New("list failed"))
			},
			wantError: "list failed",
		},
		{
			name: "delete error",
			setupMock: func(sp *secretsmocks.MockProvider) {
				sp.EXPECT().ListSecrets(gomock.Any()).Return([]secrets.SecretDescription{
					{Key: llmRefreshKey},
				}, nil)
				sp.EXPECT().DeleteSecrets(gomock.Any(), []string{llmRefreshKey}).Return(errors.New("delete failed"))
			},
			wantError: "delete failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			sp := secretsmocks.NewMockProvider(ctrl)
			sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
				CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
			}).AnyTimes()
			tt.setupMock(sp)

			provider := &stubConfigUpdater{cfg: fullSetupConfig("cursor")}
			gm := &stubGatewayManager{}
			var stdout, stderr bytes.Buffer
			err := Teardown(context.Background(), &stdout, &stderr, gm, "", false, false, provider,
				func() (secrets.Provider, error) { return sp, nil })
			require.Error(t, err)

			assert.ErrorContains(t, err, "cached token cleanup failed; no configuration changes were made")
			assert.ErrorContains(t, err, tt.wantError)
			assert.Equal(t, fullSetupConfig("cursor"), provider.cfg)
			assert.Zero(t, provider.updateCalls)
			assert.Empty(t, gm.reverted)
		})
	}
}

func TestTeardown_PurgeFailureCanRetrySameTargetedCommand(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	sp := secretsmocks.NewMockProvider(ctrl)
	sp.EXPECT().Capabilities().Return(secrets.ProviderCapabilities{
		CanRead: true, CanWrite: true, CanDelete: true, CanList: true,
	}).AnyTimes()
	gomock.InOrder(
		sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, errors.New("temporary keychain failure")),
		sp.EXPECT().ListSecrets(gomock.Any()).Return(nil, nil),
	)

	provider := &stubConfigUpdater{cfg: fullSetupConfig("cursor", "claude-code")}
	factoryCalls := 0
	getSecretsProvider := func() (secrets.Provider, error) {
		factoryCalls++
		return sp, nil
	}

	var firstStdout, firstStderr bytes.Buffer
	err := Teardown(context.Background(), &firstStdout, &firstStderr,
		&stubGatewayManager{}, "cursor", false, true, provider, getSecretsProvider)
	require.ErrorContains(t, err, "temporary keychain failure")
	assert.Equal(t, fullSetupConfig("cursor", "claude-code"), provider.cfg)
	assert.Zero(t, provider.updateCalls)

	var retryStdout, retryStderr bytes.Buffer
	err = Teardown(context.Background(), &retryStdout, &retryStderr,
		&stubGatewayManager{}, "cursor", false, true, provider, getSecretsProvider)
	require.NoError(t, err)

	assert.Equal(t, 2, factoryCalls)
	assert.Equal(t, 1, provider.updateCalls)
	require.Len(t, provider.cfg.ConfiguredTools, 1)
	assert.Equal(t, "claude-code", provider.cfg.ConfiguredTools[0].Tool)
}

// ── Setup --lazy path ─────────────────────────────────────────────────────────

// setupGatewayManager reports one or more detected clients and patches them
// successfully, for Setup-level tests. mode is returned by LLMGatewayModeFor;
// use "proxy" to avoid the direct-mode Anthropic-prefix probe.
type setupGatewayManager struct {
	detected   []string
	mode       string
	hint       string
	configured []string
	applied    []llmgateway.ApplyConfig
}

func (g *setupGatewayManager) DetectedLLMGatewayClients() []string { return g.detected }
func (g *setupGatewayManager) ConfigureLLMGateway(client string, cfg llmgateway.ApplyConfig) (string, error) {
	g.configured = append(g.configured, client)
	g.applied = append(g.applied, cfg)
	return "/tmp/settings.json", nil
}
func (g *setupGatewayManager) LLMGatewayModeFor(_ string) string { return g.mode }
func (*setupGatewayManager) PromptCacheConflict(_ string) (string, error) {
	return "", nil
}
func (*setupGatewayManager) IsManaged(_ string) bool { return false }
func (g *setupGatewayManager) LLMClientDetectionHint(_ string) string {
	return g.hint
}
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
		SetOptions{}, "", true, "", true, nil,
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

func TestSetup_Lazy_VSCodeSelectionSemantics(t *testing.T) {
	t.Parallel()

	t.Run("explicit VS Code client fails before login", func(t *testing.T) {
		t.Parallel()
		gm := &setupGatewayManager{detected: []string{"vscode"}, mode: llmgateway.ModeVSCode}
		provider := configuredSetupProvider()
		loginCalled := false
		var stdout, stderr bytes.Buffer
		err := Setup(context.Background(), &stdout, &stderr, gm, provider,
			func(_ context.Context, _ *Config) error { loginCalled = true; return nil },
			SetOptions{}, "", true, "vscode", true, nil)
		require.ErrorContains(t, err, "requires non-lazy setup for authenticated model discovery")
		assert.False(t, loginCalled)
		assert.Empty(t, gm.configured)
	})

	t.Run("multi-client setup warns, skips model-discovery clients, and continues", func(t *testing.T) {
		t.Parallel()
		gm := &setupGatewayManager{detected: []string{"vscode", "pi", "cursor", "vscode-insider"}, mode: llmgateway.ModeProxy}
		provider := configuredSetupProvider()
		loginCalled := false
		var stdout, stderr bytes.Buffer
		err := Setup(context.Background(), &stdout, &stderr, gm, provider,
			func(_ context.Context, _ *Config) error { loginCalled = true; return nil },
			SetOptions{}, "", true, "", true, nil)
		require.NoError(t, err)
		assert.False(t, loginCalled)
		assert.Equal(t, []string{"cursor"}, gm.configured)
		assert.Contains(t, stderr.String(), "skipping vscode in lazy mode")
		assert.Contains(t, stderr.String(), "skipping vscode-insider in lazy mode")
		assert.Contains(t, stderr.String(), "skipping pi in lazy mode")
		require.Len(t, provider.cfg.ConfiguredTools, 1)
		assert.Equal(t, "cursor", provider.cfg.ConfiguredTools[0].Tool)
	})
}

func TestSetupPiModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer discovery-token", r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5","context_window":400000}]}`))
		case "/anthropic/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-5","max_input_tokens":1000000,` +
				`"capabilities":{"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}}],` +
				`"has_more":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	for _, tt := range []struct {
		name    string
		lazy    bool
		short   bool
		models  []string
		wantErr string
	}{
		{name: "discover models"},
		{name: "explicit Anthropic model", models: []string{"claude-sonnet-5"}},
		{name: "short cache does not suggest long", short: true},
		{name: "lazy no models", lazy: true, wantErr: "does not support --lazy"},
		{name: "lazy explicit models", lazy: true, models: []string{"claude-sonnet-5"}, wantErr: "does not support --lazy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &setupGatewayManager{detected: []string{"pi"}, mode: llmgateway.ModePi}
			provider := configuredSetupProvider()
			provider.cfg.GatewayURL = server.URL
			provider.cfg.TLSSkipVerify = true
			var out, errOut bytes.Buffer
			opts := SetOptions{Models: tt.models}
			if tt.short {
				opts.ShortPromptCache = &tt.short
			}
			err := Setup(context.Background(), &out, &errOut, gm, provider,
				func(_ context.Context, cfg *Config) error {
					cfg.SetDiscoveryAccessToken("discovery-token")
					return nil
				}, opts, "/anthropic", true, "pi", tt.lazy, nil)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Zero(t, provider.updateCalls)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, !tt.short, strings.Contains(out.String(), "PI_CACHE_RETENTION=long"))
			require.Len(t, gm.applied, 1)
			assert.Equal(t, tt.models, gm.applied[0].Models)
			assert.Equal(t, []string{"gpt-5"}, gm.applied[0].DiscoveredModels)
			assert.Equal(t, map[string]int{"gpt-5": 400000}, gm.applied[0].DiscoveredModelContextWindows)
			assert.Equal(t, []string{"claude-sonnet-5"}, gm.applied[0].AnthropicModels)
			assert.Equal(t, map[string]int{"claude-sonnet-5": 1000000}, gm.applied[0].AnthropicModelContextWindows)
			assert.Equal(t, map[string]bool{"claude-sonnet-5": true}, gm.applied[0].AnthropicModelReasoning)
			assert.Equal(t, map[string]bool{"claude-sonnet-5": true}, gm.applied[0].AnthropicModelAdaptive)
			assert.Equal(t, server.URL+"/anthropic", gm.applied[0].AnthropicBaseURL)
		})
	}
}

func TestSetupPiAnthropicPrefix(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, flag, wantPath, wantBase string
		flagSet                        bool
	}{
		{name: "root", flagSet: true, wantPath: "/v1/models"},
		{name: "custom", flag: "/custom", flagSet: true, wantPath: "/custom/v1/models", wantBase: "/custom"},
		{name: "probed", wantPath: "/anthropic/v1/models", wantBase: "/anthropic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodHead && r.URL.Path == "/anthropic/v1/messages":
					w.WriteHeader(http.StatusUnauthorized)
				case r.URL.Path == "/v1/models" && r.Method == http.MethodGet && tt.wantPath != "/v1/models":
					w.WriteHeader(http.StatusNotFound)
				case r.URL.Path == tt.wantPath && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"data":[{"id":"claude-test"}],"has_more":false}`))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			gm := &setupGatewayManager{detected: []string{"pi"}, mode: llmgateway.ModePi}
			provider := configuredSetupProvider()
			provider.cfg.GatewayURL = server.URL
			provider.cfg.TLSSkipVerify = true
			var out, errOut bytes.Buffer
			err := Setup(context.Background(), &out, &errOut, gm, provider,
				func(_ context.Context, cfg *Config) error { cfg.SetDiscoveryAccessToken("token"); return nil },
				SetOptions{}, tt.flag, tt.flagSet, "pi", false, nil)
			require.NoError(t, err)
			require.Len(t, gm.applied, 1)
			assert.Equal(t, []string{"claude-test"}, gm.applied[0].AnthropicModels)
			wantBase := ""
			if tt.wantBase != "" {
				wantBase = server.URL + tt.wantBase
			}
			assert.Equal(t, wantBase, gm.applied[0].AnthropicBaseURL)
		})
	}
}

func TestDiscoverSetupModelsPiAnthropicFallback(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		assert.Equal(t, "/anthropic/v1/models", r.URL.Path)
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-opus-4-8","max_input_tokens":1000000}],"has_more":false}`))
	}))
	t.Cleanup(server.Close)
	cfg := Config{GatewayURL: server.URL}
	cfg.SetDiscoveryAccessToken("discovery-token")
	var warnings strings.Builder
	clients, catalog, err := discoverSetupModels(context.Background(), &warnings, []string{"pi"}, "pi", cfg, "/anthropic")
	require.NoError(t, err)
	assert.Equal(t, []string{"pi"}, clients)
	assert.Empty(t, catalog.openAI)
	assert.Equal(t, []string{"claude-opus-4-8"}, catalog.anthropic)
	assert.Equal(t, map[string]int{"claude-opus-4-8": 1000000}, catalog.anthropicContextWindows)
	assert.Contains(t, warnings.String(), "skipping Pi OpenAI models")
}

func TestDiscoverSetupModelsBedrockClaudeCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		compat        bool
		token         string
		status        int
		targetClient  string
		clients       []string
		models        []string
		body          string
		wantRequested bool
		wantErr       string
		wantClients   []string
		wantAnthropic []string
		wantWarning   string
	}{
		{
			name:          "bedrock compat fetches the Anthropic catalogue",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusOK,
			targetClient:  claudeCodeClient,
			clients:       []string{claudeCodeClient},
			wantRequested: true,
			wantClients:   []string{claudeCodeClient},
			wantAnthropic: []string{"us.anthropic.claude-opus-4-8"},
		},
		{
			name:          "explicit claude-code fails when discovery fails",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusInternalServerError,
			targetClient:  claudeCodeClient,
			clients:       []string{claudeCodeClient},
			wantRequested: true,
			wantErr:       "discovering Bedrock models for Claude Code",
		},
		{
			name:          "auto-detected claude-code is skipped with a warning when discovery fails",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusInternalServerError,
			targetClient:  "",
			clients:       []string{claudeCodeClient, "cursor"},
			wantRequested: true,
			wantClients:   []string{"cursor"},
			wantWarning:   "skipping auto-detected claude-code because Bedrock model discovery failed",
		},
		{
			name:          "explicit claude-code fails when the catalogue has no Bedrock IDs",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusOK,
			body:          `{"data":[{"id":"claude-opus-4-8"}],"has_more":false}`,
			targetClient:  claudeCodeClient,
			clients:       []string{claudeCodeClient},
			wantRequested: true,
			wantErr:       "the gateway's model list has no Bedrock model IDs",
		},
		{
			name:          "auto-detected claude-code is skipped when the catalogue has no Bedrock IDs",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusOK,
			body:          `{"data":[{"id":"claude-opus-4-8"}],"has_more":false}`,
			clients:       []string{claudeCodeClient, "cursor"},
			wantRequested: true,
			wantClients:   []string{"cursor"},
			wantWarning:   "the gateway's model list has no Bedrock model IDs",
		},
		{
			name:          "models pinning every tier keep claude-code when discovery fails",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusInternalServerError,
			targetClient:  claudeCodeClient,
			clients:       []string{claudeCodeClient},
			models:        []string{"us.anthropic.claude-haiku-4-5", "us.anthropic.claude-opus-4-8", "us.anthropic.claude-sonnet-5", "us.anthropic.claude-fable-5-1"},
			wantRequested: true,
			wantClients:   []string{claudeCodeClient},
		},
		{
			name:          "models pinning some tiers do not bypass a discovery failure",
			compat:        true,
			token:         "discovery-token",
			status:        http.StatusInternalServerError,
			targetClient:  claudeCodeClient,
			clients:       []string{claudeCodeClient},
			models:        []string{"us.anthropic.claude-opus-4-8"},
			wantRequested: true,
			wantErr:       "discovering Bedrock models for Claude Code",
		},
		{
			name:         "no request without bedrock compat",
			token:        "discovery-token",
			targetClient: claudeCodeClient,
			clients:      []string{claudeCodeClient},
			wantClients:  []string{claudeCodeClient},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requested atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested.Store(true)
				assert.Equal(t, "/anthropic/v1/models", r.URL.Path)
				if tt.status != http.StatusOK {
					w.WriteHeader(tt.status)
					return
				}
				body := tt.body
				if body == "" {
					body = `{"data":[{"id":"us.anthropic.claude-opus-4-8"}],"has_more":false}`
				}
				_, _ = w.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			cfg := Config{GatewayURL: server.URL, Models: tt.models, Bedrock: BedrockConfig{Compat: tt.compat}}
			cfg.SetDiscoveryAccessToken(tt.token)
			var warnings strings.Builder

			clients, catalog, err := discoverSetupModels(
				context.Background(), &warnings, tt.clients, tt.targetClient, cfg, "/anthropic")

			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.NotContains(t, err.Error(), "--models")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantClients, clients)
			assert.Equal(t, tt.wantRequested, requested.Load())
			if tt.wantAnthropic != nil {
				assert.Equal(t, tt.wantAnthropic, catalog.anthropic)
			}
			if tt.wantWarning == "" {
				assert.Empty(t, warnings.String())
				return
			}
			assert.Contains(t, warnings.String(), tt.wantWarning)
		})
	}
}

func TestFilterLazyModelDiscoveryClientsBedrock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		clients       []string
		targetClient  string
		bedrockCompat bool
		hasModels     bool
		wantErr       string
		wantClients   []string
		wantWarning   string
	}{
		{
			name:          "explicit claude-code with bedrock-compat and no models is refused",
			clients:       []string{claudeCodeClient},
			targetClient:  claudeCodeClient,
			bedrockCompat: true,
			wantErr:       "does not support --lazy with --bedrock-compat",
		},
		{
			name:          "auto-detected claude-code with bedrock-compat and no models is skipped",
			clients:       []string{claudeCodeClient, "cursor"},
			bedrockCompat: true,
			wantClients:   []string{"cursor"},
			wantWarning:   "skipping claude-code in lazy mode; --bedrock-compat needs",
		},
		{
			name:          "claude-code with bedrock-compat and models is kept",
			clients:       []string{claudeCodeClient},
			targetClient:  claudeCodeClient,
			bedrockCompat: true,
			hasModels:     true,
			wantClients:   []string{claudeCodeClient},
		},
		{
			name:         "claude-code without bedrock-compat is kept",
			clients:      []string{claudeCodeClient},
			targetClient: claudeCodeClient,
			wantClients:  []string{claudeCodeClient},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var warnings strings.Builder
			got, err := filterLazyModelDiscoveryClients(
				tt.clients, tt.targetClient, tt.bedrockCompat, tt.hasModels, &warnings)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantClients, got)
			if tt.wantWarning == "" {
				assert.Empty(t, warnings.String())
				return
			}
			assert.Contains(t, warnings.String(), tt.wantWarning)
		})
	}
}

func TestDiscoverAnthropicGatewayModels(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/anthropic/v1/models", r.URL.Path)
		assert.Equal(t, "Bearer discovery-token", r.Header.Get("Authorization"))
		switch r.URL.Query().Get("after_id") {
		case "":
			_, _ = w.Write([]byte(`{"data":[{"id":" claude-opus-4-8 ","max_input_tokens":1000000,` +
				`"capabilities":{"thinking":{"supported":true,"types":{"adaptive":{"supported":true}}}}},` +
				`{"id":"claude-haiku-5-5","capabilities":{"thinking":{"supported":false}}}],` +
				`"has_more":true,"last_id":"claude-haiku-5-5"}`))
		case "claude-haiku-5-5":
			_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-5","max_input_tokens":200000},` +
				`{"id":"claude-haiku-4-5","capabilities":{"thinking":{"supported":true,` +
				`"types":{"enabled":{"supported":true}}}}},` +
				`{"id":"claude-opus-4-8","max_input_tokens":10}],"has_more":false}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	cfg := Config{GatewayURL: server.URL}
	cfg.SetDiscoveryAccessToken("discovery-token")
	models, contexts, reasoning, adaptive, err := discoverAnthropicGatewayModels(context.Background(), cfg, "/anthropic")
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-haiku-4-5", "claude-haiku-5-5", "claude-opus-4-8", "claude-sonnet-5"}, models)
	assert.Equal(t, map[string]int{"claude-opus-4-8": 1000000, "claude-sonnet-5": 200000}, contexts)
	assert.Equal(t, map[string]bool{"claude-opus-4-8": true, "claude-haiku-4-5": true,
		"claude-haiku-5-5": false}, reasoning)
	assert.Equal(t, map[string]bool{"claude-opus-4-8": true, "claude-haiku-4-5": false,
		"claude-haiku-5-5": false}, adaptive)
}

func TestDiscoverAnthropicGatewayModelsRejectsInvalidPagination(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"model"}],"has_more":true}`))
	}))
	t.Cleanup(server.Close)
	cfg := Config{GatewayURL: server.URL}
	cfg.SetDiscoveryAccessToken("discovery-token")
	_, _, _, _, err := discoverAnthropicGatewayModels(context.Background(), cfg, "/anthropic")
	require.ErrorContains(t, err, "invalid Anthropic pagination cursor")
}

func TestDiscoverGatewayModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		body        string
		setToken    bool
		want        []string
		wantContext map[string]int
		wantErr     string
	}{
		{name: "valid OpenAI list is trimmed, deduplicated, and sorted", status: http.StatusOK,
			body: `{"object":"list","data":[{"id":" z-model ","context_window":1000000},` +
				`{"id":"a-model"},{"id":"z-model","context_window":-5},` +
				`{"id":" a-model ","context_window":0}]}`,
			setToken: true, want: []string{"a-model", "z-model"},
			wantContext: map[string]int{"z-model": 1000000}},
		{name: "missing authentication", status: http.StatusOK, body: `{"object":"list","data":[{"id":"model"}]}`,
			wantErr: "did not provide an access token"},
		{name: "authentication failure", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`,
			setToken: true, wantErr: "401 Unauthorized"},
		{name: "malformed response", status: http.StatusOK, body: `{`, setToken: true,
			wantErr: "decoding gateway model response"},
		{name: "wrong response shape", status: http.StatusOK, body: `{"object":"other","data":[{"id":"model"}]}`,
			setToken: true, wantErr: "expected a non-empty OpenAI model list"},
		{name: "empty model ID", status: http.StatusOK, body: `{"object":"list","data":[{"id":" "}]}`,
			setToken: true, wantErr: "model ID must not be empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/models", r.URL.Path)
				if tt.setToken {
					assert.Equal(t, "Bearer discovery-token", r.Header.Get("Authorization"))
				}
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			cfg := Config{GatewayURL: server.URL}
			if tt.setToken {
				cfg.SetDiscoveryAccessToken("discovery-token")
			}
			got, contexts, err := discoverGatewayModels(context.Background(), cfg)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantContext, contexts)
		})
	}
}

func TestModelDiscoveryErrorMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		anthropic bool
		status    int
		body      string
		closed    bool
		wantErr   string
		wantIs    error
	}{
		{name: "unreachable gateway", anthropic: true, closed: true,
			wantErr: "could not reach the gateway at"},
		{name: "Anthropic list not found", anthropic: true, status: http.StatusNotFound,
			wantErr: "doesn't serve an Anthropic model list at"},
		{name: "OpenAI list not found", status: http.StatusNotFound,
			wantErr: "doesn't serve a model list at"},
		{name: "unauthorized", anthropic: true, status: http.StatusUnauthorized,
			wantErr: "the gateway rejected model discovery (401 Unauthorized)"},
		{name: "forbidden", status: http.StatusForbidden,
			wantErr: "the gateway rejected model discovery (403 Forbidden)"},
		{name: "gateway error", anthropic: true, status: http.StatusBadGateway,
			wantErr: "the gateway returned 502 Bad Gateway listing models; retry, or contact the gateway operator"},
		{name: "empty Anthropic catalogue", anthropic: true, status: http.StatusOK,
			body:    `{"data":[],"has_more":false}`,
			wantErr: "the gateway returned no Anthropic models for your account", wantIs: errEmptyModelCatalog},
		{name: "empty OpenAI catalogue", status: http.StatusOK, body: `{"object":"list","data":[]}`,
			wantErr: "the gateway returned no models for your account", wantIs: errEmptyModelCatalog},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			if tt.closed {
				server.Close()
			}
			// A password in the gateway URL's userinfo must never reach a diagnostic.
			cfg := Config{GatewayURL: strings.Replace(server.URL, "://", "://alice:secret-pw@", 1)}
			cfg.SetDiscoveryAccessToken("discovery-token")

			var err error
			if tt.anthropic {
				_, _, _, _, err = discoverAnthropicGatewayModels(context.Background(), cfg, "/anthropic")
			} else {
				_, _, err = discoverGatewayModels(context.Background(), cfg)
			}
			require.ErrorContains(t, err, tt.wantErr)
			assert.NotContains(t, err.Error(), "secret-pw")
			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}

func TestSetup_VSCodeDiscoversModelsAfterLogin(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer discovery-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"model-b"},{"id":"model-a"}]}`))
	}))
	t.Cleanup(server.Close)
	gm := &setupGatewayManager{detected: []string{"vscode"}, mode: llmgateway.ModeVSCode}
	provider := configuredSetupProvider()
	provider.cfg.GatewayURL = server.URL
	provider.cfg.TLSSkipVerify = true
	var stdout, stderr bytes.Buffer
	err := Setup(context.Background(), &stdout, &stderr, gm, provider,
		func(_ context.Context, cfg *Config) error { cfg.SetDiscoveryAccessToken("discovery-token"); return nil },
		SetOptions{}, "", true, "vscode", false, nil)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)
	assert.Equal(t, []string{"model-a", "model-b"}, gm.applied[0].DiscoveredModels)
	assert.Equal(t, "http://localhost:14000/v1", gm.applied[0].ProxyBaseURL)
}

func TestSetup_VSCodeDiscoveryFailureSelectionSemantics(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	providerForTest := func() *stubConfigUpdater {
		provider := configuredSetupProvider()
		provider.cfg.GatewayURL = server.URL
		provider.cfg.TLSSkipVerify = true
		return provider
	}

	for _, clientType := range []string{"vscode", "vscode-insider"} {
		t.Run("explicit "+clientType+" client fails", func(t *testing.T) {
			t.Parallel()
			gm := &setupGatewayManager{detected: []string{clientType}, mode: llmgateway.ModeVSCode}
			provider := providerForTest()
			var stdout, stderr bytes.Buffer
			err := Setup(context.Background(), &stdout, &stderr, gm, provider,
				func(_ context.Context, cfg *Config) error { cfg.SetDiscoveryAccessToken("token"); return nil },
				SetOptions{}, "", true, clientType, false, nil)
			require.ErrorContains(t, err, "discovering gateway models")
			require.ErrorContains(t, err, "503 Service Unavailable")
			assert.Empty(t, gm.configured)
			assert.Zero(t, provider.updateCalls)
		})
	}

	t.Run("auto-detected VS Code is skipped while other clients continue", func(t *testing.T) {
		t.Parallel()
		gm := &setupGatewayManager{detected: []string{"vscode", "cursor", "vscode-insider"}, mode: llmgateway.ModeProxy}
		provider := providerForTest()
		var stdout, stderr bytes.Buffer
		err := Setup(context.Background(), &stdout, &stderr, gm, provider,
			func(_ context.Context, cfg *Config) error { cfg.SetDiscoveryAccessToken("token"); return nil },
			SetOptions{}, "", true, "", false, nil)
		require.NoError(t, err)
		assert.Equal(t, []string{"cursor"}, gm.configured)
		assert.Contains(t, stderr.String(), "skipping auto-detected VS Code clients")
		assert.Contains(t, stderr.String(), "gateway model discovery failed")
		assert.Contains(t, stderr.String(), "503 Service Unavailable")
		require.Len(t, provider.cfg.ConfiguredTools, 1)
		assert.Equal(t, "cursor", provider.cfg.ConfiguredTools[0].Tool)
	})

	t.Run("auto-detected VS Code alone is skipped without persistence", func(t *testing.T) {
		t.Parallel()
		gm := &setupGatewayManager{detected: []string{"vscode"}, mode: llmgateway.ModeVSCode}
		provider := providerForTest()
		var stdout, stderr bytes.Buffer
		err := Setup(context.Background(), &stdout, &stderr, gm, provider,
			func(_ context.Context, cfg *Config) error { cfg.SetDiscoveryAccessToken("token"); return nil },
			SetOptions{}, "", true, "", false, nil)
		require.NoError(t, err)
		assert.Empty(t, gm.configured)
		assert.Zero(t, provider.updateCalls)
		assert.Contains(t, stderr.String(), "skipping auto-detected VS Code clients")
	})
}

func TestSetup_ShortPromptCachePreference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		persistedShort bool
		inline         *bool
		wantShort      bool
	}{
		{name: "default applies one-hour cache"},
		{name: "persisted opt-out is reapplied", persistedShort: true, wantShort: true},
		{name: "inline opt-out is persisted", inline: boolPtr(true), wantShort: true},
		{name: "explicit false restores one-hour cache", persistedShort: true, inline: boolPtr(false)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &capturingGatewayManager{detected: []string{claudeCodeClient}, mode: llmgateway.ModeDirect}
			provider := configuredSetupProvider()
			provider.cfg.ShortPromptCache = tt.persistedShort
			var stdout, stderr bytes.Buffer
			err := Setup(
				context.Background(), &stdout, &stderr, gm, provider,
				func(context.Context, *Config) error { return nil },
				SetOptions{ShortPromptCache: tt.inline}, "", true, "", true, nil,
			)
			require.NoError(t, err)
			require.Len(t, gm.applied, 1)
			assert.Equal(t, tt.wantShort, gm.applied[0].ShortPromptCache)
			assert.Equal(t, tt.wantShort, provider.cfg.ShortPromptCache)
		})
	}
}

func TestSetup_ShortPromptCacheWarningForNonClaudeClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		inline   *bool
		wantWarn bool
	}{
		{name: "plain setup is silent"},
		{name: "explicit short-cache flag warns", inline: boolPtr(true), wantWarn: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &capturingGatewayManager{detected: []string{"cursor"}, mode: llmgateway.ModeProxy}
			provider := configuredSetupProvider()
			var stdout, stderr bytes.Buffer
			err := Setup(
				context.Background(), &stdout, &stderr, gm, provider,
				func(context.Context, *Config) error { return nil },
				SetOptions{ShortPromptCache: tt.inline}, "", true, "cursor", true, nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantWarn, strings.Contains(stderr.String(), "--short-prompt-cache was set"))
		})
	}
}

func TestSetup_PromptCacheBedrockWarningOnlyForInlineFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		persistedBedrock  bool
		inlineBedrock     *bool
		shortPromptCache  bool
		wantCompatibility bool
	}{
		{name: "inline Bedrock compatibility warns", inlineBedrock: boolPtr(true), wantCompatibility: true},
		{name: "persisted Bedrock compatibility is silent", persistedBedrock: true},
		{name: "short cache is compatible", inlineBedrock: boolPtr(true), shortPromptCache: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &capturingGatewayManager{detected: []string{claudeCodeClient}, mode: llmgateway.ModeDirect}
			provider := configuredSetupProvider()
			provider.cfg.Bedrock.Compat = tt.persistedBedrock
			provider.cfg.ShortPromptCache = tt.shortPromptCache
			var stdout, stderr bytes.Buffer
			// Pass --models so bedrock-compat pins the tier without gateway discovery;
			// lazy bedrock-compat setup is refused without it.
			err := Setup(
				context.Background(), &stdout, &stderr, gm, provider,
				func(context.Context, *Config) error { return nil },
				SetOptions{BedrockCompat: tt.inlineBedrock, Models: []string{"us.anthropic.claude-opus-4-8"}},
				"", true, "", true, nil,
			)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCompatibility,
				strings.Contains(stderr.String(), "may not take effect with Bedrock compatibility"))
		})
	}
}

func TestFilterDetectedClients_LeftoverDirHint(t *testing.T) {
	t.Parallel()
	gm := &setupGatewayManager{
		hint: `settings directory exists but "claude" was not found on PATH`,
	}
	_, err := filterDetectedClients(gm, "claude-code")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"claude-code" is not installed or not detected`)
	assert.Contains(t, err.Error(), `was not found on PATH`)
}

func TestFilterDetectedClients_GenericMiss(t *testing.T) {
	t.Parallel()
	_, err := filterDetectedClients(&setupGatewayManager{}, "cursor")
	require.Error(t, err)
	assert.Equal(t, `client "cursor" is not installed or not detected`, err.Error())
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
		SetOptions{}, "", true, "", false, nil,
	)
	require.NoError(t, err)

	assert.True(t, loginCalled, "non-lazy mode must invoke the OIDC login")
	require.Len(t, provider.cfg.ConfiguredTools, 1)
}

func TestSetup_NonLazy_LoginFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		loginErr        error
		wantContains    []string
		wantNotContains string
	}{
		{
			name:     "callback port in use includes remediation",
			loginErr: &networking.CallbackPortInUseError{Port: 8666},
			wantContains: []string{
				"OIDC login failed",
				"requested callback port 8666 is already in use",
				"stop the process using it",
				"thv llm setup --callback-port <port>",
			},
		},
		{
			name:            "other login errors keep generic handling",
			loginErr:        errors.New("login unavailable"),
			wantContains:    []string{"OIDC login failed", "login unavailable"},
			wantNotContains: "--callback-port",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gm := &setupGatewayManager{detected: []string{"cursor"}, mode: "proxy"}
			provider := configuredSetupProvider()
			login := func(_ context.Context, _ *Config) error { return tt.loginErr }

			var stdout, stderr bytes.Buffer
			err := Setup(
				context.Background(), &stdout, &stderr, gm, provider, login,
				SetOptions{}, "", true, "", false, nil,
			)

			require.Error(t, err)
			for _, want := range tt.wantContains {
				assert.Contains(t, err.Error(), want)
			}
			if tt.wantNotContains != "" {
				assert.NotContains(t, err.Error(), tt.wantNotContains)
			}
			assert.Empty(t, provider.cfg.ConfiguredTools)
		})
	}
}

func TestSetup_CallbackPortInUseBeforeLogin(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, listener.Close()) })
	port := listener.Addr().(*net.TCPAddr).Port

	provider := configuredSetupProvider()
	provider.cfg.OIDC.CallbackPort = port
	loginCalled := false
	login := func(_ context.Context, _ *Config) error {
		loginCalled = true
		return nil
	}

	var stdout, stderr bytes.Buffer
	err = Setup(
		context.Background(), &stdout, &stderr,
		&setupGatewayManager{detected: []string{"cursor"}, mode: "proxy"}, provider, login,
		SetOptions{}, "", true, "", false, nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid inline flag values")
	assert.NotContains(t, err.Error(), "OIDC login failed")
	assert.Contains(t, err.Error(), fmt.Sprintf("requested callback port %d is already in use", port))
	assert.Contains(t, err.Error(), "thv llm setup --callback-port <port>")
	assert.False(t, loginCalled)
}

// ── AnthropicPathPrefix / configureDetectedTools ──────────────────────────────

// capturingGatewayManager records the ApplyConfig passed to ConfigureLLMGateway.
type capturingGatewayManager struct {
	mode             string // returned by LLMGatewayModeFor
	detected         []string
	cacheConflict    string
	cacheConflictErr error
	conflictCalls    int
	applied          []llmgateway.ApplyConfig
}

func (g *capturingGatewayManager) DetectedLLMGatewayClients() []string { return g.detected }
func (g *capturingGatewayManager) ConfigureLLMGateway(_ string, cfg llmgateway.ApplyConfig) (string, error) {
	g.applied = append(g.applied, cfg)
	return "/path/to/settings.json", nil
}
func (g *capturingGatewayManager) LLMGatewayModeFor(_ string) string { return g.mode }
func (g *capturingGatewayManager) PromptCacheConflict(_ string) (string, error) {
	g.conflictCalls++
	return g.cacheConflict, g.cacheConflictErr
}
func (*capturingGatewayManager) IsManaged(_ string) bool { return false }
func (*capturingGatewayManager) LLMClientDetectionHint(_ string) string {
	return ""
}
func (*capturingGatewayManager) LLMSetupNoteFor(_ string) string    { return "" }
func (*capturingGatewayManager) RevertLLMGateway(_, _ string) error { return nil }
func (*capturingGatewayManager) ConfigureEnvFile(_ string, _ llmgateway.ApplyConfig) (string, error) {
	return "", nil
}
func (*capturingGatewayManager) RevertEnvFile(_, _ string) error { return nil }

func TestConfigureDetectedTools_PathPrefixAppendedForDirectMode(t *testing.T) {
	t.Parallel()

	gm := &capturingGatewayManager{mode: "direct"}
	var out, errOut bytes.Buffer

	_, err := configureDetectedToolsWithDiscovery(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1",
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "/anthropic", nil, discoveredGatewayModels{}, false,
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

	_, err := configureDetectedToolsWithDiscovery(
		&out, &errOut, gm,
		[]string{"claude-code"},
		"https://gw.example.com", "http://localhost:14000/v1",
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "", nil, discoveredGatewayModels{}, false, // no prefix
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

	_, err := configureDetectedToolsWithDiscovery(
		&out, &errOut, gm,
		[]string{"cursor"},
		"https://gw.example.com", "http://localhost:14000/v1",
		"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
		false, "/anthropic", nil, discoveredGatewayModels{}, false,
		BedrockConfig{},
	)
	require.NoError(t, err)
	require.Len(t, gm.applied, 1)

	// Proxy-mode tools must never receive an AnthropicBaseURL.
	assert.Empty(t, gm.applied[0].AnthropicBaseURL)
}

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

func TestVSCodeModeRequiresProxyAndUsesProxyTLSWarning(t *testing.T) {
	t.Parallel()

	configured := []ToolConfig{{Tool: "vscode", Mode: llmgateway.ModeVSCode}}
	assert.True(t, hasProxyMode(configured))

	var errOut bytes.Buffer
	warnTLSSkipVerify(&errOut, true, configured)
	assert.Contains(t, errOut.String(), "proxy's upstream gateway connection only")
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
func (*managedGatewayManager) PromptCacheConflict(_ string) (string, error) {
	return "", nil
}
func (g *managedGatewayManager) IsManaged(c string) bool { return g.managed[c] }
func (*managedGatewayManager) LLMClientDetectionHint(_ string) string {
	return ""
}
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
	assert.Contains(t, errOut.String(), "managed configuration for claude-desktop")
	assert.NotContains(t, errOut.String(), "profile for other-desktop")
}
