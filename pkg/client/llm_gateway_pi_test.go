// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llmgateway"
	"github.com/stacklok/toolhive/pkg/skills"
)

func TestPiGatewaySetupAndTeardown(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	path := filepath.Join(home, ".pi", "agent", "models.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"theme":"dark","providers":{"personal":{"models":[{"id":"local"}]}}}`), 0o600))
	cfg := llmgateway.ApplyConfig{
		GatewayURL:                    "https://gateway.example/",
		AnthropicBaseURL:              "https://gateway.example/custom",
		DiscoveredModels:              []string{"anthropic/claude-sonnet-5", "gpt-5"},
		DiscoveredModelContextWindows: map[string]int{"anthropic/claude-sonnet-5": 1000000},
		AnthropicModels:               []string{"claude-opus-4-8", "claude-haiku-5-5", "claude-haiku-4-5"},
		AnthropicModelContextWindows:  map[string]int{"claude-opus-4-8": 1000000},
		AnthropicModelReasoning:       map[string]bool{"claude-opus-4-8": true, "claude-haiku-4-5": true},
		AnthropicModelAdaptive:        map[string]bool{"claude-opus-4-8": true},
	}
	for range 2 {
		got, err := cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
		require.NoError(t, err)
		require.Equal(t, path, got)
	}
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	var root struct {
		Theme     string `json:"theme"`
		Providers map[string]struct {
			BaseURL    string `json:"baseUrl"`
			API        string `json:"api"`
			APIKey     string `json:"apiKey"`
			AuthHeader bool   `json:"authHeader"`
			Models     []struct {
				ID            string `json:"id"`
				ContextWindow *int   `json:"contextWindow"`
				Reasoning     *bool  `json:"reasoning"`
				Compat        *struct {
					ForceAdaptiveThinking bool `json:"forceAdaptiveThinking"`
				} `json:"compat"`
			} `json:"models"`
		} `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(content, &root))
	require.Equal(t, "dark", root.Theme)
	require.Equal(t, "local", root.Providers["personal"].Models[0].ID)
	require.Equal(t, "https://gateway.example/v1", root.Providers[piProviderName].BaseURL)
	require.Equal(t, "openai-completions", root.Providers[piProviderName].API)
	require.Equal(t, piTokenCommand, root.Providers[piProviderName].APIKey)
	require.True(t, root.Providers[piProviderName].AuthHeader)
	require.Equal(t, "anthropic/claude-sonnet-5", root.Providers[piProviderName].Models[0].ID)
	require.NotNil(t, root.Providers[piProviderName].Models[0].ContextWindow)
	require.Equal(t, 1000000, *root.Providers[piProviderName].Models[0].ContextWindow)
	require.Equal(t, "gpt-5", root.Providers[piProviderName].Models[1].ID)
	require.Nil(t, root.Providers[piProviderName].Models[1].ContextWindow)
	require.Nil(t, root.Providers[piProviderName].Models[0].Reasoning)
	require.Nil(t, root.Providers[piProviderName].Models[1].Reasoning)
	require.Nil(t, root.Providers[piProviderName].Models[0].Compat)
	require.Equal(t, "https://gateway.example/custom", root.Providers[piAnthropicProviderName].BaseURL)
	require.Equal(t, "anthropic-messages", root.Providers[piAnthropicProviderName].API)
	require.Equal(t, piTokenCommand, root.Providers[piAnthropicProviderName].APIKey)
	require.True(t, root.Providers[piAnthropicProviderName].AuthHeader)
	require.Equal(t, "claude-opus-4-8", root.Providers[piAnthropicProviderName].Models[0].ID)
	require.NotNil(t, root.Providers[piAnthropicProviderName].Models[0].ContextWindow)
	require.Equal(t, 1000000, *root.Providers[piAnthropicProviderName].Models[0].ContextWindow)
	require.NotNil(t, root.Providers[piAnthropicProviderName].Models[0].Reasoning)
	require.True(t, *root.Providers[piAnthropicProviderName].Models[0].Reasoning)
	require.NotNil(t, root.Providers[piAnthropicProviderName].Models[0].Compat)
	require.True(t, root.Providers[piAnthropicProviderName].Models[0].Compat.ForceAdaptiveThinking)
	require.Nil(t, root.Providers[piAnthropicProviderName].Models[1].ContextWindow)
	require.Nil(t, root.Providers[piAnthropicProviderName].Models[1].Reasoning)
	require.Nil(t, root.Providers[piAnthropicProviderName].Models[1].Compat)
	require.NotNil(t, root.Providers[piAnthropicProviderName].Models[2].Reasoning)
	require.True(t, *root.Providers[piAnthropicProviderName].Models[2].Reasoning)
	require.Nil(t, root.Providers[piAnthropicProviderName].Models[2].Compat)

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Pi), path))
	content, err = os.ReadFile(path)
	require.NoError(t, err)
	root.Providers = nil
	require.NoError(t, json.Unmarshal(content, &root))
	require.NotContains(t, root.Providers, piProviderName)
	require.NotContains(t, root.Providers, piAnthropicProviderName)
	require.Contains(t, root.Providers, "personal")
}

func TestPiGatewayPreservesJSONCAndBOM(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	path := filepath.Join(home, ".pi", "agent", "models.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	original := append(append([]byte{}, piBOM...), []byte(`{
  // Keep this comment on the root object.
  "theme": "dark",
  "providers": {
    // Keep this personal provider comment.
    "personal": {
      "models": [
        { "id": "local" }, // Keep this model comment.
      ],
    },
  },
}`)...)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	cfg := llmgateway.ApplyConfig{GatewayURL: "https://gateway.example", DiscoveredModels: []string{"gpt-5"}}
	_, err := cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.NoError(t, err)
	assertPreserved := func() {
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(content, piBOM))
		for _, comment := range []string{"Keep this comment on the root object.",
			"Keep this personal provider comment.", "Keep this model comment."} {
			require.Contains(t, string(content), comment)
		}
		_, providers, err := readPiConfig(path)
		require.NoError(t, err)
		require.Contains(t, providers, "personal")
	}
	assertPreserved()
	require.NoError(t, cm.RevertLLMGateway(ClientApp(Pi), path))
	assertPreserved()
}

func TestPiGatewayAgentDirOverride(t *testing.T) {
	home, agentDir := t.TempDir(), filepath.Join(t.TempDir(), "agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cm.lookPath = func(string) (string, error) { return "/usr/local/bin/pi", nil }
	require.NotContains(t, cm.DetectedLLMGatewayClients(), ClientApp(Pi))
	require.NoError(t, os.MkdirAll(agentDir, 0o700))
	require.Contains(t, cm.DetectedLLMGatewayClients(), ClientApp(Pi))
	path, err := cm.ConfigureLLMGateway(ClientApp(Pi), llmgateway.ApplyConfig{
		GatewayURL: "https://gateway.example", DiscoveredModels: []string{"gpt-5"},
	})
	require.NoError(t, err)
	require.Equal(t, filepath.Join(agentDir, "models.json"), path)
	require.NoFileExists(t, filepath.Join(home, ".pi", "agent", "models.json"))
	require.NoError(t, cm.RevertLLMGateway(ClientApp(Pi), path))
	_, providers, err := readPiConfig(path)
	require.NoError(t, err)
	require.NotContains(t, providers, piProviderName)
	t.Setenv("PI_CODING_AGENT_DIR", "")
	require.NotContains(t, cm.DetectedLLMGatewayClients(), ClientApp(Pi))
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o700))
	require.Contains(t, cm.DetectedLLMGatewayClients(), ClientApp(Pi))
	t.Setenv("PI_CODING_AGENT_DIR", "~/custom-pi-agent")
	require.Equal(t, filepath.Join(home, "custom-pi-agent", "models.json"),
		cm.buildLLMSettingsPath(cm.lookupClientAppConfig(ClientApp(Pi))))
	t.Setenv("PI_CODING_AGENT_DIR", "relative-pi-agent")
	relativePath, err := filepath.Abs(filepath.Join("relative-pi-agent", "models.json"))
	require.NoError(t, err)
	require.Equal(t, relativePath, cm.buildLLMSettingsPath(cm.lookupClientAppConfig(ClientApp(Pi))))
}

// TestPiAgentDirOverrideAppliesToMCPAndSkills pins that PI_CODING_AGENT_DIR
// relocates Pi's MCP config and global skills alongside models.json, so
// `thv client register` and `thv skill install` land in the same directory as
// `thv llm setup`.
func TestPiAgentDirOverrideAppliesToMCPAndSkills(t *testing.T) {
	home, agentDir := t.TempDir(), filepath.Join(t.TempDir(), "agent")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)

	mcpPath, err := cm.GetConfigPath(ClientApp(Pi))
	require.NoError(t, err)
	require.Equal(t, filepath.Join(agentDir, "mcp.json"), mcpPath)

	skillPath, err := cm.GetSkillPath(ClientApp(Pi), "my-skill", skills.ScopeUser, "")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(agentDir, "skills", "my-skill"), skillPath)

	require.False(t, cm.IsClientInstalled(ClientApp(Pi)))
	require.NoError(t, os.MkdirAll(agentDir, 0o700))
	require.True(t, cm.IsClientInstalled(ClientApp(Pi)))

	_, err = cm.CreateClientConfig(ClientApp(Pi))
	require.NoError(t, err)
	require.FileExists(t, mcpPath)
	require.NoFileExists(t, filepath.Join(home, ".pi", "agent", "mcp.json"))
}

func TestPiGatewayAnthropicOnly(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cfg := llmgateway.ApplyConfig{
		GatewayURL:                   "https://gateway.example",
		Models:                       []string{"claude-opus-4-8"},
		DiscoveredModels:             []string{"gpt-5"},
		AnthropicModels:              []string{"claude-opus-4-8"},
		AnthropicModelContextWindows: map[string]int{"claude-opus-4-8": 1000000},
	}
	path, err := cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.NoError(t, err)
	_, providers, err := readPiConfig(path)
	require.NoError(t, err)
	require.NotContains(t, providers, piProviderName)
	require.Contains(t, providers, piAnthropicProviderName)
	var anthropic struct {
		BaseURL string `json:"baseUrl"`
	}
	require.NoError(t, json.Unmarshal(providers[piAnthropicProviderName], &anthropic))
	require.Equal(t, cfg.GatewayURL, anthropic.BaseURL)
	require.NoError(t, cm.RevertLLMGateway(ClientApp(Pi), path))
	_, providers, err = readPiConfig(path)
	require.NoError(t, err)
	require.NotContains(t, providers, piAnthropicProviderName)
}

func TestPiGatewayRejectsUnmanagedAnthropicProvider(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	path := filepath.Join(home, ".pi", "agent", "models.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	original := []byte(`{"providers":{"toolhive-anthropic":{"apiKey":"personal","models":[{"id":"my-model"}]}}}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	cfg := llmgateway.ApplyConfig{GatewayURL: "https://gateway.example", DiscoveredModels: []string{"gpt-5"}}
	_, err := cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.ErrorContains(t, err, "not managed")
	require.ErrorContains(t, cm.RevertLLMGateway(ClientApp(Pi), path), "no longer managed")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, content)
}

func TestPiGatewayRejectsUnclassifiedModel(t *testing.T) {
	t.Parallel()
	cm := NewTestClientManager(t.TempDir(), nil, supportedClientIntegrations, nil)
	cfg := llmgateway.ApplyConfig{GatewayURL: "https://gateway.example", Models: []string{"claude-opus-4-8"}}
	_, err := cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.ErrorContains(t, err, "cannot determine its API")
}

func TestPiGatewayRejectsUnownedProviderAndMissingModels(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	cm := NewTestClientManager(home, nil, supportedClientIntegrations, nil)
	cfg := llmgateway.ApplyConfig{GatewayURL: "https://gateway.example"}
	_, err := cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.ErrorContains(t, err, "requires a model list")
	path := filepath.Join(home, ".pi", "agent", "models.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	original := []byte(`{"providers":{"toolhive":{"apiKey":"personal","models":[{"id":"my-model"}]}}}`)
	require.NoError(t, os.WriteFile(path, original, 0o600))
	cfg.Models = []string{"claude-sonnet-5"}
	cfg.DiscoveredModels = []string{"claude-sonnet-5"}
	_, err = cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.ErrorContains(t, err, "not managed")
	require.ErrorContains(t, cm.RevertLLMGateway(ClientApp(Pi), path), "no longer managed")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, content)
}
