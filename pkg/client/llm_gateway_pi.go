// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/llmgateway"
)

const piProviderName = "toolhive"
const piAnthropicProviderName = "toolhive-anthropic"
const piTokenCommand = "!thv llm token" //nolint:gosec // command, not a credential

// configurePi adds a named provider without disturbing the user's other providers.
func (cm *ClientManager) configurePi(appCfg *clientAppConfig, cfg llmgateway.ApplyConfig) (string, error) {
	openAIModels, anthropicModels := selectPiModels(cfg)
	if len(openAIModels) == 0 && len(anthropicModels) == 0 {
		return "", fmt.Errorf("pi requires a model list; pass --models or run setup without --lazy for gateway model discovery")
	}
	path := cm.buildLLMSettingsPath(appCfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating Pi config directory: %w", err)
	}
	err := fileutils.WithFileLock(path, func() error {
		root, providers, err := readPiConfig(path)
		if err != nil {
			return err
		}
		for _, name := range []string{piProviderName, piAnthropicProviderName} {
			if existing, ok := providers[name]; ok && !piProviderManaged(existing) {
				return fmt.Errorf("pi provider %q already exists and is not managed by ToolHive", name)
			}
		}
		baseURL := strings.TrimRight(cfg.GatewayURL, "/")
		if len(openAIModels) > 0 {
			provider, err := encodePiGatewayProvider(baseURL+"/v1", "openai-completions", openAIModels,
				cfg.DiscoveredModelContextWindows)
			if err != nil {
				return err
			}
			providers[piProviderName] = provider
		} else {
			delete(providers, piProviderName)
		}
		if len(anthropicModels) > 0 {
			provider, err := encodePiGatewayProvider(baseURL+"/anthropic", "anthropic-messages", anthropicModels,
				cfg.AnthropicModelContextWindows)
			if err != nil {
				return err
			}
			providers[piAnthropicProviderName] = provider
		} else {
			delete(providers, piAnthropicProviderName)
		}
		return writePiConfig(path, root, providers)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

func selectPiModels(cfg llmgateway.ApplyConfig) ([]string, []string) {
	if len(cfg.Models) == 0 {
		return cfg.DiscoveredModels, cfg.AnthropicModels
	}
	var openAIModels, anthropicModels []string
	for _, id := range cfg.Models {
		if slices.Contains(cfg.DiscoveredModels, id) || !slices.Contains(cfg.AnthropicModels, id) {
			openAIModels = append(openAIModels, id)
		}
		if slices.Contains(cfg.AnthropicModels, id) {
			anthropicModels = append(anthropicModels, id)
		}
	}
	return openAIModels, anthropicModels
}

func piProviderManaged(existing json.RawMessage) bool {
	var provider struct {
		APIKey string `json:"apiKey"`
	}
	return json.Unmarshal(existing, &provider) == nil && provider.APIKey == piTokenCommand
}

func encodePiGatewayProvider(baseURL, api string, models []string, contextWindows map[string]int) (json.RawMessage, error) {
	type piModel struct {
		ID            string `json:"id"`
		ContextWindow int    `json:"contextWindow,omitempty"`
	}
	entries := make([]piModel, 0, len(models))
	for _, id := range models {
		entries = append(entries, piModel{ID: id, ContextWindow: contextWindows[id]})
	}
	provider, err := json.Marshal(struct {
		BaseURL    string    `json:"baseUrl"`
		API        string    `json:"api"`
		APIKey     string    `json:"apiKey"`
		AuthHeader bool      `json:"authHeader"`
		Models     []piModel `json:"models"`
	}{baseURL, api, piTokenCommand, true, entries})
	if err != nil {
		return nil, fmt.Errorf("encoding Pi provider: %w", err)
	}
	return provider, nil
}

func (cm *ClientManager) revertPi(appCfg *clientAppConfig, path string) error {
	if path != cm.buildLLMSettingsPath(appCfg) {
		return fmt.Errorf("refusing to revert Pi config at unexpected path %q", path)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("checking Pi config: %w", err)
	}
	return fileutils.WithFileLock(path, func() error {
		root, providers, err := readPiConfig(path)
		if err != nil {
			return err
		}
		owned := false
		for _, name := range []string{piProviderName, piAnthropicProviderName} {
			existing, ok := providers[name]
			if !ok {
				continue
			}
			owned = true
			if !piProviderManaged(existing) {
				return fmt.Errorf("pi provider %q is no longer managed by ToolHive", name)
			}
		}
		if !owned {
			return nil
		}
		delete(providers, piProviderName)
		delete(providers, piAnthropicProviderName)
		return writePiConfig(path, root, providers)
	})
}

func readPiConfig(path string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	root := make(map[string]json.RawMessage)
	content, err := os.ReadFile(path) // #nosec G304 -- path is the registered Pi config location
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("reading Pi config: %w", err)
	}
	if len(content) != 0 {
		if err := json.Unmarshal(content, &root); err != nil || root == nil {
			return nil, nil, fmt.Errorf("decoding Pi config %s: expected a JSON object: %v", path, err)
		}
	}
	providers := make(map[string]json.RawMessage)
	if raw, ok := root["providers"]; ok {
		if err := json.Unmarshal(raw, &providers); err != nil || providers == nil {
			return nil, nil, fmt.Errorf("decoding Pi providers in %s: expected a JSON object: %v", path, err)
		}
	}
	return root, providers, nil
}

func writePiConfig(path string, root, providers map[string]json.RawMessage) error {
	encoded, err := json.Marshal(providers)
	if err != nil {
		return fmt.Errorf("encoding Pi providers: %w", err)
	}
	root["providers"] = encoded
	content, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding Pi config: %w", err)
	}
	if err := fileutils.AtomicWriteFile(path, append(content, '\n'), 0o600); err != nil {
		return fmt.Errorf("writing Pi config: %w", err)
	}
	return nil
}
