// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/tailscale/hujson"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/llmgateway"
)

const (
	vsCodeProviderName   = "ToolHive"
	vsCodeProviderVendor = "customendpoint"
	// The OpenAI-compatible /v1/models response exposes IDs but not token limits.
	// VS Code requires positive values, so ToolHive supplies conservative defaults.
	vsCodeMaxInputTokens  = 128000
	vsCodeMaxOutputTokens = 16000
)

type vsCodeProviderGroup struct {
	Name   string        `json:"name"`
	Vendor string        `json:"vendor"`
	Models []vsCodeModel `json:"models"`
}

type vsCodeModel struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	URL             string            `json:"url"`
	ToolCalling     bool              `json:"toolCalling"`
	Vision          bool              `json:"vision"`
	MaxInputTokens  int               `json:"maxInputTokens"`
	MaxOutputTokens int               `json:"maxOutputTokens"`
	RequestHeaders  map[string]string `json:"requestHeaders"`
}

func (cm *ClientManager) configureVSCode(appCfg *clientAppConfig, cfg llmgateway.ApplyConfig) (string, error) {
	if len(cfg.DiscoveredModels) == 0 {
		return "", fmt.Errorf("configuring %s: gateway model discovery returned no models", appCfg.ClientType)
	}
	path := cm.buildLLMSettingsPath(appCfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating directory for %s: %w", path, err)
	}

	models := make([]vsCodeModel, 0, len(cfg.DiscoveredModels))
	for _, id := range cfg.DiscoveredModels {
		models = append(models, vsCodeModel{
			ID: id, Name: id, URL: cfg.ProxyBaseURL,
			ToolCalling: true, Vision: false,
			MaxInputTokens: vsCodeMaxInputTokens, MaxOutputTokens: vsCodeMaxOutputTokens,
			RequestHeaders: map[string]string{"Authorization": "Bearer " + llmPlaceholderAPIKey},
		})
	}
	group, err := json.Marshal(vsCodeProviderGroup{
		Name: vsCodeProviderName, Vendor: vsCodeProviderVendor, Models: models,
	})
	if err != nil {
		return "", fmt.Errorf("marshaling VS Code provider group: %w", err)
	}

	err = fileutils.WithFileLock(path, func() error {
		content, err := readOrInitFile(path, []byte("[]"))
		if err != nil {
			return err
		}
		v, err := hujson.Parse(content)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		if err := patchVSCodeProviderGroups(&v, path, group); err != nil {
			return err
		}
		formatted, err := hujson.Format(v.Pack())
		if err != nil {
			return fmt.Errorf("formatting %s: %w", path, err)
		}
		return fileutils.AtomicWriteFile(path, formatted, 0o600)
	})
	if err != nil {
		return "", err
	}

	// Cleanup is best-effort after the authoritative new configuration is
	// durable. A failure must not make setup lose the path needed by teardown.
	if err := removeLegacyVSCodeSettings(filepath.Join(filepath.Dir(path), "settings.json")); err != nil {
		slog.Warn("Could not remove obsolete VS Code Copilot settings", "error", err)
	}
	return path, nil
}

func (*ClientManager) revertVSCode(_ *clientAppConfig, configPath string) error {
	legacyPath := filepath.Join(filepath.Dir(configPath), "settings.json")
	if filepath.Base(configPath) == "settings.json" {
		legacyPath = configPath
	}
	if err := removeLegacyVSCodeSettings(legacyPath); err != nil {
		return err
	}
	// Older ToolHive versions persisted settings.json as ConfigPath. That file
	// is an object and cannot contain a provider group from the new integration.
	if filepath.Base(configPath) == "settings.json" {
		return nil
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil
	}
	return fileutils.WithFileLock(configPath, func() error {
		content, err := os.ReadFile(configPath) // #nosec G304 -- persisted configuration path
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("reading %s: %w", configPath, err)
		}
		v, err := hujson.Parse(content)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}
		if err := patchVSCodeProviderGroups(&v, configPath, nil); err != nil {
			return err
		}
		formatted, err := hujson.Format(v.Pack())
		if err != nil {
			return fmt.Errorf("formatting %s: %w", configPath, err)
		}
		return fileutils.AtomicWriteFile(configPath, formatted, 0o600)
	})
}

// patchVSCodeProviderGroups removes all ToolHive-owned provider groups and,
// when replacement is non-nil, appends the replacement. Patching the hujson
// syntax tree preserves comments in unrelated groups.
func patchVSCodeProviderGroups(v *hujson.Value, path string, replacement json.RawMessage) error {
	standardized, err := hujson.Standardize(v.Pack())
	if err != nil {
		return fmt.Errorf("standardizing %s: %w", path, err)
	}
	var groups []json.RawMessage
	if err := json.Unmarshal(standardized, &groups); err != nil {
		return fmt.Errorf("parsing %s: expected an array of provider groups: %w", path, err)
	}

	operations := make([]llmPatchOp, 0, len(groups)+1)
	for i := len(groups) - 1; i >= 0; i-- {
		var identity struct {
			Name   string `json:"name"`
			Vendor string `json:"vendor"`
		}
		if err := json.Unmarshal(groups[i], &identity); err != nil {
			return fmt.Errorf("parsing provider group in %s: %w", path, err)
		}
		if identity.Name == vsCodeProviderName && identity.Vendor == vsCodeProviderVendor {
			operations = append(operations, llmPatchOp{Op: "remove", Path: fmt.Sprintf("/%d", i)})
		}
	}
	if replacement != nil {
		operations = append(operations, llmPatchOp{Op: "add", Path: "/-", Value: replacement})
	}
	if len(operations) == 0 {
		return nil
	}
	patch, err := json.Marshal(operations)
	if err != nil {
		return fmt.Errorf("marshaling provider-group patch for %s: %w", path, err)
	}
	if err := v.Patch(patch); err != nil {
		return fmt.Errorf("patching provider groups in %s: %w", path, err)
	}
	return nil
}

func removeLegacyVSCodeSettings(path string) error {
	legacy := &clientAppConfig{LLMGatewayKeys: []LLMGatewayKeySpec{
		{JSONPointer: "/github.copilot.advanced.serverUrl"},
		{JSONPointer: "/github.copilot.advanced.apiKey"},
	}}
	if err := revertJSONPointerGateway(legacy, path); err != nil {
		return fmt.Errorf("removing obsolete VS Code Copilot settings: %w", err)
	}
	return nil
}
