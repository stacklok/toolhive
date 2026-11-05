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

// The config.toml keys a Bedrock setup writes. Bedrock's OpenAI-compatible API
// ends the stream with an error when a request carries Codex's hosted
// web_search tool, so web_search must be "disabled" (the [tools] web_search
// toggle does not stop Codex sending it). The memory models are the background
// tiers Codex otherwise defaults to OpenAI names for (gpt-5.6-terra and
// gpt-5.6-luna), which a Bedrock-only gateway does not serve.
//
// ToolHive treats these keys as its own, the way it treats Claude Code's
// Bedrock env keys (ClearWhenEmpty): setup writes them in Bedrock mode and
// removes them otherwise, and revert removes them whatever they hold.
const (
	codexModelKey              = "model"
	codexWebSearchKey          = "web_search"
	codexContextWindowKey      = "model_context_window"
	codexMemoriesKey           = "memories"
	codexConsolidationModelKey = "consolidation_model"
	codexExtractModelKey       = "extract_model"
	codexWebSearchDisabled     = "disabled"
)

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

		if err := applyCodexBedrockKeys(config, cfg, path); err != nil {
			return err
		}

		config["model_provider"] = codexProviderID

		// The revert path makes the same table check before touching it.
		providers, err := existingTOMLTable(config, "model_providers", path)
		if err != nil {
			return err
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
// revertCredentialHelper's appliedId check for Claude Desktop. It also removes
// the Bedrock keys (model, web_search, model_context_window and the memory
// models) whatever they hold, the same as Claude Code's Bedrock env keys. A
// missing file is treated as already-reverted.
//
// Revert removes these keys rather than restoring whatever they were before
// setup: configureCodexAuth does not stash the prior values, so a user who had
// e.g. model_provider = "openai" or their own model before setup is left with
// none after revert (Codex then falls back to its own default).
func (*ClientManager) revertCodexAuth(_ *clientAppConfig, configPath string) error {
	if configPath == "" || !fileExistsAt(configPath) {
		return nil
	}

	return fileutils.WithFileLock(configPath, func() error {
		config, err := readTOMLConfig(configPath)
		if err != nil {
			return err
		}

		clearCodexBedrockKeys(config)

		if providers, ok := config["model_providers"].(map[string]any); ok {
			delete(providers, codexProviderID)
			setOrDeleteTable(config, "model_providers", providers)
		}

		if provider, ok := config["model_provider"].(string); ok && provider == codexProviderID {
			delete(config, "model_provider")
		}

		return writeTOMLConfig(configPath, config)
	})
}

// applyCodexBedrockKeys writes the Bedrock keys into config when cfg is a
// Bedrock setup, and removes them otherwise, so a re-run that turns Bedrock off
// leaves no stale model behind. pkg/llm resolves every value; this only writes
// them. It refuses to overwrite a memories entry that is not a table, as
// configureCodexAuth does for model_providers.
func applyCodexBedrockKeys(config map[string]any, cfg llmgateway.ApplyConfig, path string) error {
	if !cfg.BedrockCompat || cfg.BedrockCodexModel == "" {
		clearCodexBedrockKeys(config)
		return nil
	}
	memories, err := existingTOMLTable(config, codexMemoriesKey, path)
	if err != nil {
		return err
	}

	config[codexModelKey] = cfg.BedrockCodexModel
	config[codexWebSearchKey] = codexWebSearchDisabled
	if cfg.BedrockCodexContextWindow > 0 {
		config[codexContextWindowKey] = int64(cfg.BedrockCodexContextWindow)
	} else {
		delete(config, codexContextWindowKey)
	}
	setOrDeleteKey(memories, codexConsolidationModelKey, cfg.BedrockCodexConsolidationModel)
	setOrDeleteKey(memories, codexExtractModelKey, cfg.BedrockCodexExtractModel)
	setOrDeleteTable(config, codexMemoriesKey, memories)
	return nil
}

// clearCodexBedrockKeys removes every Bedrock key from config, dropping the
// memories table only when nothing else is left in it.
func clearCodexBedrockKeys(config map[string]any) {
	delete(config, codexModelKey)
	delete(config, codexWebSearchKey)
	delete(config, codexContextWindowKey)
	if memories, ok := config[codexMemoriesKey].(map[string]any); ok {
		delete(memories, codexConsolidationModelKey)
		delete(memories, codexExtractModelKey)
		setOrDeleteTable(config, codexMemoriesKey, memories)
	}
}

// existingTOMLTable returns the table stored under key in config, or a new
// empty one when key is absent. It refuses an entry that is not a table
// (a malformed file, or a future Codex schema), since writing a table over it
// would silently destroy the user's config.
func existingTOMLTable(config map[string]any, key, path string) (map[string]any, error) {
	existing, present := config[key]
	if !present {
		return map[string]any{}, nil
	}
	table, ok := existing.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("existing %s in %s is not a table (%T); refusing to overwrite", key, path, existing)
	}
	return table, nil
}

// setOrDeleteKey sets table[key] to value, or removes the key when value is empty.
func setOrDeleteKey(table map[string]any, key, value string) {
	if value == "" {
		delete(table, key)
		return
	}
	table[key] = value
}

// setOrDeleteTable stores table under key, or removes the key when table is empty.
func setOrDeleteTable(config map[string]any, key string, table map[string]any) {
	if len(table) == 0 {
		delete(config, key)
		return
	}
	config[key] = table
}
