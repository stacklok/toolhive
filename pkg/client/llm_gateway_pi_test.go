// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/llmgateway"
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
		DiscoveredModels:              []string{"anthropic/claude-sonnet-5", "gpt-5"},
		DiscoveredModelContextWindows: map[string]int{"anthropic/claude-sonnet-5": 1000000},
		AnthropicModels:               []string{"claude-opus-4-8", "claude-haiku-5-5"},
		AnthropicModelContextWindows:  map[string]int{"claude-opus-4-8": 1000000},
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
	require.Equal(t, "https://gateway.example/anthropic", root.Providers[piAnthropicProviderName].BaseURL)
	require.Equal(t, "anthropic-messages", root.Providers[piAnthropicProviderName].API)
	require.Equal(t, piTokenCommand, root.Providers[piAnthropicProviderName].APIKey)
	require.True(t, root.Providers[piAnthropicProviderName].AuthHeader)
	require.Equal(t, "claude-opus-4-8", root.Providers[piAnthropicProviderName].Models[0].ID)
	require.NotNil(t, root.Providers[piAnthropicProviderName].Models[0].ContextWindow)
	require.Equal(t, 1000000, *root.Providers[piAnthropicProviderName].Models[0].ContextWindow)
	require.Nil(t, root.Providers[piAnthropicProviderName].Models[1].ContextWindow)

	require.NoError(t, cm.RevertLLMGateway(ClientApp(Pi), path))
	content, err = os.ReadFile(path)
	require.NoError(t, err)
	root.Providers = nil
	require.NoError(t, json.Unmarshal(content, &root))
	require.NotContains(t, root.Providers, piProviderName)
	require.NotContains(t, root.Providers, piAnthropicProviderName)
	require.Contains(t, root.Providers, "personal")
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
	_, err = cm.ConfigureLLMGateway(ClientApp(Pi), cfg)
	require.ErrorContains(t, err, "not managed")
	require.ErrorContains(t, cm.RevertLLMGateway(ClientApp(Pi), path), "no longer managed")
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, content)
}
