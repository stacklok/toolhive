// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package llm

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llmgateway"
)

func TestResolveCodexTiers(t *testing.T) {
	t.Parallel()

	discovered := []string{
		"us.openai.gpt-6.1-sol", "us.openai.gpt-6.1-terra", "us.openai.gpt-6-luna", "qwen.qwen3-coder-480b",
	}
	newest := codexTiers{sol: "us.openai.gpt-6.1-sol", terra: "us.openai.gpt-6.1-terra", luna: "us.openai.gpt-6-luna"}

	tests := []struct {
		name         string
		models       []string
		discovered   []string
		want         codexTiers
		wantWarnings []string
		wantErr      string
	}{
		{name: "no models and no discovery resolve nothing"},
		{name: "discovery fills each tier", discovered: discovered, want: newest},
		{
			name:       "entries for other tools leave the discovered tiers",
			models:     []string{"us.anthropic.claude-opus-4-8", "claude-sonnet-5", "qwen.qwen3-coder-480b"},
			discovered: discovered,
			want:       newest,
		},
		{
			name:       "a pin overrides the tier its family names",
			models:     []string{"us.anthropic.claude-opus-4-8", "global.openai.gpt-5.6-terra-v1:0"},
			discovered: discovered,
			want:       codexTiers{sol: newest.sol, terra: "global.openai.gpt-5.6-terra-v1:0", luna: newest.luna},
		},
		{
			name:   "a pin naming no family pins the main model",
			models: []string{"us.openai.gpt-6-astra"},
			want:   codexTiers{sol: "us.openai.gpt-6-astra"},
		},
		{
			name:   "a second pin for a tier wins with a warning",
			models: []string{"us.openai.gpt-6-sol", "us.openai.gpt-6.1-sol"},
			want:   codexTiers{sol: "us.openai.gpt-6.1-sol"},
			wantWarnings: []string{
				`--models entry "us.openai.gpt-6.1-sol" overrides "us.openai.gpt-6-sol" for the Codex sol tier; ` +
					`only the last is used`,
			},
		},
		{
			// A --models list shared with Pi, which can use the open-weight models.
			name:       "gpt-oss entries are left to other tools",
			models:     []string{"us.openai.gpt-oss-120b", "OpenAI.GPT-OSS-20B"},
			discovered: discovered,
			want:       newest,
		},
		{
			name:    "a mixed-case pin is refused with its lowercase form",
			models:  []string{"US.OpenAI.GPT-6.1-Terra"},
			wantErr: `"US.OpenAI.GPT-6.1-Terra" is not lowercase; Bedrock model IDs are, so use "us.openai.gpt-6.1-terra"`,
		},
		{
			name:       "an invalid pin is refused even when discovery has models",
			models:     []string{"openai.gpt-6.1-sol"},
			discovered: discovered,
			wantErr:    "has no inference-profile prefix; Bedrock serves GPT models through us. or global. profiles (e.g. us.openai.gpt-6.1-sol)",
		},
		{
			name:    "a mixed-case GPT model without a profile prefix is refused with the prefixed form",
			models:  []string{"OpenAI.gpt-6.1-sol"},
			wantErr: "has no inference-profile prefix; Bedrock serves GPT models through us. or global. profiles (e.g. us.openai.gpt-6.1-sol)",
		},
		{
			name:    "a GPT ID that is not a Bedrock profile is refused",
			models:  []string{"gpt-6.1"},
			wantErr: `"gpt-6.1" is not a Bedrock GPT inference profile`,
		},
		{
			name:    "the first invalid entry is reported",
			models:  []string{"gpt-6.1", "openai.gpt-6.1-sol"},
			wantErr: `"gpt-6.1"`,
		},
		{
			name:   "a valid pin wins over an invalid entry, which is ignored with a warning",
			models: []string{"gpt-6.1", "us.openai.gpt-6.1-sol"},
			want:   codexTiers{sol: "us.openai.gpt-6.1-sol"},
			wantWarnings: []string{
				`--models entry "gpt-6.1" is not a Bedrock GPT inference profile; only those serve the ` +
					`Responses API Codex speaks. To use Codex, pin a Bedrock GPT inference profile such as ` +
					`us.openai.gpt-6.1-sol, or drop it from --models to use the newest one the gateway lists; ignored`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, warnings, err := resolveCodexTiers(tt.models, tt.discovered)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantWarnings, warnings)
		})
	}
}

func TestNewestCodexTiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		discovered []string
		want       codexTiers
	}{
		{name: "empty listing has no model"},
		{
			name: "each family keeps its own newest version",
			discovered: []string{
				"us.openai.gpt-5.6-sol", "us.openai.gpt-6.1-sol", "us.openai.gpt-5.6-terra", "us.openai.gpt-6-luna",
				"us.openai.gpt-5.6-luna",
			},
			want: codexTiers{sol: "us.openai.gpt-6.1-sol", terra: "us.openai.gpt-5.6-terra", luna: "us.openai.gpt-6-luna"},
		},
		{
			// Ranking across families sent the main model to luna here, since
			// both are version 6.0 and "luna" sorts first.
			name:       "a luna of the same version never displaces sol",
			discovered: []string{"us.openai.gpt-6-luna", "us.openai.gpt-6-sol"},
			want:       codexTiers{sol: "us.openai.gpt-6-sol", luna: "us.openai.gpt-6-luna"},
		},
		{
			name:       "ties go to the smallest ID",
			discovered: []string{"us.openai.gpt-6.1-sol", "global.openai.gpt-6.1-sol"},
			want:       codexTiers{sol: "global.openai.gpt-6.1-sol"},
		},
		{
			name: "open-weight, chat-only, unprefixed, and family-less models are skipped",
			discovered: []string{
				"us.openai.gpt-oss-120b", "openai.gpt-oss-20b", "nvidia.nemotron-super-v1",
				"qwen.qwen3-coder-480b", "deepseek.v3", "openai.gpt-7-sol", "gpt-7-sol", "us.openai.gpt-6-astra",
				"us.openai.gpt-6-luna",
			},
			want: codexTiers{luna: "us.openai.gpt-6-luna"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, newestCodexTiers(tt.discovered))
		})
	}
}

func TestFilterCodexBedrockPin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		clients       []string
		targetClient  string
		bedrockCompat bool
		models        []string
		wantClients   []string
		wantErr       string
		wantWarning   string
	}{
		{
			name:        "an invalid pin is ignored without bedrock-compat",
			clients:     []string{codexClient},
			models:      []string{"qwen.qwen3"},
			wantClients: []string{codexClient},
		},
		{
			name:          "a valid pin keeps codex",
			clients:       []string{codexClient, "cursor"},
			bedrockCompat: true,
			models:        []string{"us.openai.gpt-6.1-sol"},
			wantClients:   []string{codexClient, "cursor"},
		},
		{
			name:          "an invalid pin for explicit codex is refused",
			clients:       []string{codexClient},
			targetClient:  codexClient,
			bedrockCompat: true,
			models:        []string{"openai.gpt-6.1-sol"},
			wantErr:       "configuring Codex for Bedrock: --models entry \"openai.gpt-6.1-sol\" has no inference-profile prefix",
		},
		{
			name:          "an invalid pin skips auto-detected codex with a warning",
			clients:       []string{"cursor", codexClient},
			bedrockCompat: true,
			models:        []string{"openai.gpt-6.1-sol"},
			wantClients:   []string{"cursor"},
			wantWarning:   "Warning: skipping auto-detected codex: --models entry \"openai.gpt-6.1-sol\"",
		},
		{
			name:          "other vendors' and gpt-oss entries are left to other tools and keep explicit codex",
			clients:       []string{"pi", codexClient},
			targetClient:  codexClient,
			bedrockCompat: true,
			models:        []string{"nvidia.nemotron-super-v1", "us.openai.gpt-oss-120b"},
			wantClients:   []string{"pi", codexClient},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var warnings strings.Builder
			got, err := filterCodexBedrockPin(tt.clients, tt.targetClient, tt.bedrockCompat, tt.models, &warnings)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
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

// TestSetupClients_CodexPinFilter checks that when the pre-login pin check drops
// the only detected client, setup reports nothing left to do instead of going
// on to log in.
func TestSetupClients_CodexPinFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		detected    []string
		lazy        bool
		wantClients []string
		wantDone    bool
	}{
		{name: "the only client dropped means done", detected: []string{codexClient}, wantDone: true},
		{name: "the only client dropped in lazy mode means done", detected: []string{codexClient}, lazy: true, wantDone: true},
		{name: "other clients carry on", detected: []string{codexClient, "cursor"}, wantClients: []string{"cursor"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &setupGatewayManager{detected: tt.detected, mode: llmgateway.ModeProxy}
			var out, errOut bytes.Buffer

			clients, done, err := setupClients(
				gm, "", tt.lazy, true, []string{"openai.gpt-6.1-sol"}, &out, &errOut)

			require.NoError(t, err)
			assert.Equal(t, tt.wantDone, done)
			if tt.wantDone {
				assert.Empty(t, clients)
			} else {
				assert.Equal(t, tt.wantClients, clients)
			}
			assert.Contains(t, errOut.String(), "Warning: skipping auto-detected codex")
		})
	}
}

func TestDiscoverSetupModelsBedrockCodex(t *testing.T) {
	t.Parallel()

	const listing = `{"object":"list","data":[` +
		`{"id":"us.openai.gpt-6.1-sol","context_window":400000},` +
		`{"id":"qwen.qwen3-coder-480b"}]}`
	const openWeightOnly = `{"object":"list","data":[` +
		`{"id":"us.openai.gpt-oss-120b"},` +
		`{"id":"nvidia.nemotron-super-v1"}]}`

	tests := []struct {
		name          string
		token         string
		status        int
		body          string
		targetClient  string
		clients       []string
		models        []string
		wantRequested bool
		wantClients   []string
		wantWindows   map[string]int
		wantErr       string
		wantWarnings  []string
	}{
		{
			name: "bedrock compat lists the OpenAI models with their context windows", token: "discovery-token",
			status: http.StatusOK, body: listing, targetClient: codexClient, clients: []string{codexClient},
			wantRequested: true, wantClients: []string{codexClient},
			wantWindows: map[string]int{"us.openai.gpt-6.1-sol": 400000},
		},
		{
			name: "explicit codex fails when no model serves Responses", token: "discovery-token",
			status: http.StatusOK, body: openWeightOnly, targetClient: codexClient, clients: []string{codexClient},
			wantRequested: true, wantErr: "discovering Bedrock models for Codex: the gateway's model list has no sol, terra, or luna Bedrock GPT model",
		},
		{
			name: "auto-detected codex is skipped when no model serves Responses", token: "discovery-token",
			status: http.StatusOK, body: openWeightOnly, clients: []string{codexClient, "cursor"},
			wantRequested: true, wantClients: []string{"cursor"},
			wantWarnings: []string{"skipping auto-detected codex because Bedrock model discovery failed"},
		},
		{
			name:  "auto-detected claude-code and codex are each skipped when both lookups fail",
			token: "discovery-token", status: http.StatusInternalServerError,
			clients:       []string{claudeCodeClient, codexClient, "cursor"},
			wantRequested: true, wantClients: []string{"cursor"},
			wantWarnings: []string{
				"skipping auto-detected claude-code because Bedrock model discovery failed",
				"skipping auto-detected codex because Bedrock model discovery failed",
			},
		},
		{
			name: "explicit codex fails when discovery fails", token: "discovery-token",
			status: http.StatusInternalServerError, targetClient: codexClient, clients: []string{codexClient},
			wantRequested: true, wantErr: "discovering Bedrock models for Codex",
		},
		{
			name: "a pin keeps codex when discovery fails, with a metadata warning", token: "discovery-token",
			status: http.StatusInternalServerError, targetClient: codexClient, clients: []string{codexClient},
			models: []string{"us.openai.gpt-6-luna"}, wantRequested: true, wantClients: []string{codexClient},
			wantWarnings: []string{"could not read Codex model metadata from the gateway"},
		},
		{
			name: "lazy mode does not list models", targetClient: codexClient, clients: []string{codexClient},
			models: []string{"us.openai.gpt-6-luna"}, wantClients: []string{codexClient},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requested atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested.Store(true)
				assert.Equal(t, "/v1/models", r.URL.Path)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			cfg := Config{GatewayURL: server.URL, Models: tt.models, Bedrock: BedrockConfig{Compat: true}}
			cfg.SetDiscoveryAccessToken(tt.token)
			var warnings strings.Builder

			clients, catalog, err := discoverSetupModels(
				context.Background(), &warnings, tt.clients, tt.targetClient, cfg, "")

			assert.Equal(t, tt.wantRequested, requested.Load())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantClients, clients)
			if tt.wantWindows != nil {
				assert.Equal(t, tt.wantWindows, catalog.openAIContextWindows)
			}
			if len(tt.wantWarnings) == 0 {
				assert.Empty(t, warnings.String())
				return
			}
			for _, w := range tt.wantWarnings {
				assert.Contains(t, warnings.String(), w)
			}
		})
	}
}

// TestConfigureDetectedTools_BedrockCodex checks that bedrock-compat resolves
// Codex's tiers into the ApplyConfig and leaves them empty without compat, that
// the tool record says whether this run wrote them, and that the previous
// setup's record decides whether a run without compat clears them.
func TestConfigureDetectedTools_BedrockCodex(t *testing.T) {
	t.Parallel()

	discovered := discoveredGatewayModels{
		openAI:               []string{"us.openai.gpt-6.1-sol", "us.openai.gpt-6.1-terra", "us.openai.gpt-6-luna"},
		openAIContextWindows: map[string]int{"us.openai.gpt-6.1-sol": 400000},
	}
	solOnly := discoveredGatewayModels{
		openAI:               []string{"us.openai.gpt-6.1-sol"},
		openAIContextWindows: map[string]int{"us.openai.gpt-6.1-sol": 400000},
	}
	noSol := discoveredGatewayModels{
		openAI:               []string{"us.openai.gpt-6.1-terra", "us.openai.gpt-6-luna"},
		openAIContextWindows: map[string]int{"us.openai.gpt-6.1-terra": 272000},
	}

	tests := []struct {
		name              string
		compat            bool
		targetClient      string
		previousBedrock   bool
		models            []string
		discovered        discoveredGatewayModels
		wantModel         string
		wantContext       int
		wantConsolidation string
		wantExtract       string
		wantErr           string
		wantWarning       string
	}{
		{
			name: "bedrock compat pins each tier from discovery", compat: true, discovered: discovered,
			wantModel: "us.openai.gpt-6.1-sol", wantContext: 400000,
			wantConsolidation: "us.openai.gpt-6.1-terra", wantExtract: "us.openai.gpt-6-luna",
		},
		{
			name: "a missing tier falls back to the main model with a warning", compat: true, discovered: solOnly,
			wantModel: "us.openai.gpt-6.1-sol", wantContext: 400000,
			wantConsolidation: "us.openai.gpt-6.1-sol", wantExtract: "us.openai.gpt-6.1-sol",
			wantWarning: "no Bedrock model found in the gateway catalogue for the Codex terra, luna tier(s)",
		},
		{
			name: "without a sol model the main model is terra", compat: true, discovered: noSol,
			wantModel: "us.openai.gpt-6.1-terra", wantContext: 272000,
			wantConsolidation: "us.openai.gpt-6.1-terra", wantExtract: "us.openai.gpt-6-luna",
			wantWarning: "no Bedrock model found in the gateway catalogue for the Codex sol tier(s); " +
				"Codex uses us.openai.gpt-6.1-terra in their place",
		},
		{
			name: "a pin wins, with no context window when unlisted", compat: true,
			models: []string{"us.openai.gpt-6-sol"}, discovered: discovered,
			wantModel:         "us.openai.gpt-6-sol",
			wantConsolidation: "us.openai.gpt-6.1-terra", wantExtract: "us.openai.gpt-6-luna",
		},
		{
			name: "a luna pin becomes the memory extraction model", compat: true,
			models: []string{"global.openai.gpt-5.6-luna"}, discovered: discovered,
			wantModel: "us.openai.gpt-6.1-sol", wantContext: 400000,
			wantConsolidation: "us.openai.gpt-6.1-terra", wantExtract: "global.openai.gpt-5.6-luna",
		},
		{name: "without bedrock compat the tiers are left alone", discovered: discovered},
		{
			name: "after a bedrock setup, a run without compat clears the keys", discovered: discovered,
			previousBedrock: true,
		},
		{
			name: "no resolvable model skips auto-detected codex", compat: true,
			wantErr: "failed to configure any detected tools", wantWarning: "Warning: failed to configure codex",
		},
		{
			name: "no resolvable model is an error for explicit codex", compat: true, targetClient: codexClient,
			wantErr: "configuring codex for Bedrock: " + errNoCodexBedrockModels.Error(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gm := &capturingGatewayManager{mode: llmgateway.ModeCodexAuth}
			var out, errOut bytes.Buffer

			configured, err := configureDetectedToolsWithDiscovery(
				&out, &errOut, gm, []string{codexClient}, tt.targetClient,
				"https://gw.example.com", "http://localhost:14000/v1",
				"/usr/local/bin/thv", []string{"llm", "token", "--skip-browser"},
				false, "", tt.models, tt.discovered, false, BedrockConfig{Compat: tt.compat},
				map[string]bool{codexClient: tt.previousBedrock},
			)
			if tt.wantWarning != "" {
				assert.Contains(t, errOut.String(), tt.wantWarning)
			} else {
				assert.Empty(t, errOut.String())
			}
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Empty(t, gm.applied)
				return
			}
			require.NoError(t, err)
			require.Len(t, gm.applied, 1)
			got := gm.applied[0]
			assert.Equal(t, tt.compat, got.BedrockCompat)
			assert.Equal(t, tt.wantModel, got.BedrockCodexModel)
			assert.Equal(t, tt.wantContext, got.BedrockCodexContextWindow)
			assert.Equal(t, tt.wantConsolidation, got.BedrockCodexConsolidationModel)
			assert.Equal(t, tt.wantExtract, got.BedrockCodexExtractModel)
			assert.Equal(t, tt.previousBedrock, got.ClearBedrockKeys)
			require.Len(t, configured, 1)
			assert.Equal(t, new(tt.compat), configured[0].BedrockCompat)
		})
	}
}
