// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive/pkg/fileutils"
	"github.com/stacklok/toolhive/pkg/llmgateway"
)

// Codex's LLM gateway config lives in the same config.toml its MCP-server
// registration already uses (~/.codex/config.toml). Unlike the JSON-Pointer
// LLMGatewayKeys clients, Codex authenticates via a command-backed bearer
// token ([model_providers.<id>.auth]), so it gets a dedicated TOML writer
// here rather than going through the hujson JSON-Pointer machinery, which
// cannot write TOML at all.

// codexProviderID is the stable table name ToolHive owns under
// [model_providers.<id>]. Reusing a fixed id (rather than minting one per
// run) keeps repeated "thv llm setup" calls idempotent and avoids orphaned
// tables, mirroring the fixed-name approach configureCredentialHelper uses
// for Claude Desktop's _meta.json entry.
const codexProviderID = "toolhive-gateway"

// codexBaseURL returns gatewayURL with a "/v1" suffix, avoiding a double
// "/v1/v1" if the configured gateway URL already ends in one (e.g. a gateway
// mounted behind its own "/v1" path prefix).
func codexBaseURL(gatewayURL string) string {
	base := strings.TrimSuffix(gatewayURL, "/")
	if strings.HasSuffix(base, "/v1") {
		return base
	}
	return base + "/v1"
}

// configureCodexAuth writes (or updates) ToolHive's model_provider entry in
// Codex's config.toml: a custom provider pointed at the LLM gateway,
// authenticated via a command that invokes "thv llm token". Any other
// model_providers entries and the mcp_servers table (used by the unrelated
// MCP-client feature) are left untouched. Returns the config file path, which
// RevertLLMGateway later passes back to revertCodexAuth.
func (cm *ClientManager) configureCodexAuth(appCfg *clientAppConfig, cfg llmgateway.ApplyConfig) (string, error) {
	if cfg.TokenHelperPath == "" || len(cfg.TokenHelperArgs) == 0 {
		return "", fmt.Errorf("codex-auth requires TokenHelperPath and TokenHelperArgs to be set")
	}

	path := cm.buildLLMSettingsPath(appCfg)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}

	err := fileutils.WithFileLock(path, func() error {
		config, err := readTOMLConfig(path)
		if err != nil {
			return err
		}

		config["model_provider"] = codexProviderID

		// Refuse to overwrite a pre-existing model_providers that isn't a table
		// (malformed file, or a future Codex schema): clobbering it would silently
		// destroy the user's config. The revert path makes the same ok check.
		providers, ok := config["model_providers"].(map[string]any)
		if existing, present := config["model_providers"]; present && !ok {
			return fmt.Errorf("existing model_providers in %s is not a table (%T); refusing to overwrite", path, existing)
		}
		if providers == nil {
			providers = map[string]any{}
		}
		providers[codexProviderID] = map[string]any{
			"name": "ToolHive Gateway",
			// Codex's Responses-API client appends "/responses" directly to
			// base_url (the same convention OpenAI's own base_url="…/v1" uses),
			// so the gateway's "/v1" prefix must be baked in here rather than
			// left to the caller like AnthropicBaseURL's optional prefix.
			"base_url": codexBaseURL(cfg.GatewayURL),
			"wire_api": "responses",
			"auth": map[string]any{
				"command": cfg.TokenHelperPath,
				"args":    cfg.TokenHelperArgs,
				// How often Codex re-invokes the token helper. Kept in sync with the
				// TTL the other clients write (see pkg/llmgateway constants) so the
				// token source's preemptive-refresh invariant holds for Codex too.
				"refresh_interval_ms": llmgateway.CodexHelperTTL.Milliseconds(),
			},
		}
		config["model_providers"] = providers

		return writeTOMLConfig(path, config)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// revertCodexAuth undoes configureCodexAuth: it removes ToolHive's
// model_providers.<id> table (leaving any other providers untouched) and
// clears model_provider only if it still points at ToolHive's entry — a value
// changed since setup (by the user or another tool) is left alone, mirroring
// revertCredentialHelper's appliedId check for Claude Desktop. A missing file
// is treated as already-reverted.
//
// Revert removes model_provider rather than restoring whatever it was before
// setup: configureCodexAuth does not stash the prior selection, so a user who
// had e.g. model_provider = "openai" before setup is left with none after
// revert (Codex then falls back to its own default).
func (*ClientManager) revertCodexAuth(_ *clientAppConfig, configPath string) error {
	if configPath == "" || !fileExistsAt(configPath) {
		return nil
	}

	return fileutils.WithFileLock(configPath, func() error {
		config, err := readTOMLConfig(configPath)
		if err != nil {
			return err
		}

		if providers, ok := config["model_providers"].(map[string]any); ok {
			delete(providers, codexProviderID)
			if len(providers) == 0 {
				delete(config, "model_providers")
			} else {
				config["model_providers"] = providers
			}
		}

		if provider, ok := config["model_provider"].(string); ok && provider == codexProviderID {
			delete(config, "model_provider")
		}

		return writeTOMLConfig(configPath, config)
	})
}
