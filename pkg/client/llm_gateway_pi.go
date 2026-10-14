// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/llmgateway"
)

const piProviderName = "toolhive"
const piAnthropicProviderName = "toolhive-anthropic"

// Pi runs !-prefixed API keys on every request with its own fixed timeout;
// it has no configurable helper TTL or timeout to write into models.json.
const piTokenCommand = "!" + llmgateway.TokenHelperShellCommand //nolint:gosec // command, not a credential

// configurePi adds a named provider without disturbing the user's other providers.
func (cm *ClientManager) configurePi(appCfg *clientAppConfig, cfg llmgateway.ApplyConfig) (string, error) {
	openAIModels, anthropicModels, err := selectPiModels(cfg)
	if err != nil {
		return "", err
	}
	if len(openAIModels) == 0 && len(anthropicModels) == 0 {
		return "", fmt.Errorf("pi requires a model list from authenticated gateway discovery")
	}
	path := cm.buildLLMSettingsPath(appCfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating Pi config directory: %w", err)
	}
	err = fileutils.WithFileLock(path, func() error {
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
			anthropicBaseURL := baseURL
			if cfg.AnthropicBaseURL != "" {
				anthropicBaseURL = strings.TrimRight(cfg.AnthropicBaseURL, "/")
			}
			provider, err := encodePiGatewayProvider(anthropicBaseURL, "anthropic-messages", anthropicModels,
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

func selectPiModels(cfg llmgateway.ApplyConfig) ([]string, []string, error) {
	if len(cfg.Models) == 0 {
		return cfg.DiscoveredModels, cfg.AnthropicModels, nil
	}
	var openAIModels, anthropicModels []string
	for _, id := range cfg.Models {
		openAI := slices.Contains(cfg.DiscoveredModels, id)
		anthropic := slices.Contains(cfg.AnthropicModels, id)
		if !openAI && !anthropic {
			return nil, nil, fmt.Errorf("pi model %q was not found by authenticated model discovery; cannot determine its API", id)
		}
		if openAI {
			openAIModels = append(openAIModels, id)
		}
		if anthropic {
			anthropicModels = append(anthropicModels, id)
		}
	}
	return openAIModels, anthropicModels, nil
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

type piConfig struct {
	value  hujson.Value
	hasBOM bool
}

var piBOM = []byte{0xef, 0xbb, 0xbf}

func readPiConfig(path string) (piConfig, map[string]json.RawMessage, error) {
	content, err := os.ReadFile(path) // #nosec G304 -- path is the registered Pi config location
	if err != nil && !os.IsNotExist(err) {
		return piConfig{}, nil, fmt.Errorf("reading Pi config: %w", err)
	}
	hasBOM := bytes.HasPrefix(content, piBOM)
	content = bytes.TrimPrefix(content, piBOM)
	if len(content) == 0 {
		content = []byte("{}")
	}
	value, err := parseJSONC(content, path)
	if err != nil {
		return piConfig{}, nil, err
	}
	standardized, err := standardizeJSONC(&value, path)
	if err != nil {
		return piConfig{}, nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(standardized, &root); err != nil || root == nil {
		return piConfig{}, nil, fmt.Errorf("decoding Pi config %s: expected a JSON object: %v", path, err)
	}
	providers := make(map[string]json.RawMessage)
	if raw, ok := root["providers"]; ok {
		if err := json.Unmarshal(raw, &providers); err != nil || providers == nil {
			return piConfig{}, nil, fmt.Errorf("decoding Pi providers in %s: expected a JSON object: %v", path, err)
		}
	}
	return piConfig{value: value, hasBOM: hasBOM}, providers, nil
}

func writePiConfig(path string, root piConfig, providers map[string]json.RawMessage) error {
	standardized, err := standardizeJSONC(&root.value, path)
	if err != nil {
		return err
	}
	if !jsonPointerExists(standardized, "/providers") && len(providers) != 0 {
		if err := root.value.Patch([]byte(`[{"op":"add","path":"/providers","value":{}}]`)); err != nil {
			return fmt.Errorf("adding Pi providers in %s: %w", path, err)
		}
	}
	for _, name := range []string{piProviderName, piAnthropicProviderName} {
		ptr := "/providers/" + name
		value, ok := providers[name]
		if !ok {
			if err := removeLLMKey(&root.value, ptr, path, standardized); err != nil {
				return err
			}
			continue
		}
		patch, err := json.Marshal([]llmPatchOp{{Op: "add", Path: ptr, Value: value}})
		if err != nil {
			return fmt.Errorf("encoding Pi provider %q: %w", name, err)
		}
		if err := root.value.Patch(patch); err != nil {
			return fmt.Errorf("patching Pi provider %q in %s: %w", name, path, err)
		}
	}
	content, err := hujson.Format(root.value.Pack())
	if err != nil {
		return fmt.Errorf("formatting Pi config %s: %w", path, err)
	}
	if root.hasBOM {
		content = append(append([]byte{}, piBOM...), content...)
	}
	if err := fileutils.AtomicWriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("writing Pi config: %w", err)
	}
	return nil
}
