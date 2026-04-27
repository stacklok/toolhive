// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/stacklok/toolhive/pkg/fileutils"
)

// llmPlaceholderAPIKey is the static API key written into proxy-mode tool
// configurations. The localhost reverse proxy accepts any non-empty value.
const llmPlaceholderAPIKey = "thv-proxy"

// llmPatchOp is a single RFC 6902 JSON Patch operation, marshaled via
// encoding/json so all string fields are properly escaped.
type llmPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// LLMApplyConfig holds the values needed to configure an LLM gateway for a tool.
type LLMApplyConfig struct {
	GatewayURL         string // direct-mode: URL of the upstream LLM gateway
	ProxyBaseURL       string // proxy-mode: URL of the localhost reverse proxy
	TokenHelperCommand string // direct-mode: command that prints a fresh access token
}

// ConfigureLLMGateway patches the tool's LLM-gateway settings file with cfg
// and returns the absolute path of the patched file.
//
// It uses fileutils.WithFileLock so concurrent calls (e.g. two "thv llm setup"
// invocations) are serialised. Comments and trailing commas in JSONC settings
// files are preserved via hujson. Writes are crash-safe via AtomicWriteFile.
func (cm *ClientManager) ConfigureLLMGateway(clientType ClientApp, cfg LLMApplyConfig) (string, error) {
	appCfg := cm.lookupClientAppConfig(clientType)
	if appCfg == nil || appCfg.LLMGatewayMode == "" {
		return "", fmt.Errorf("client %q does not support LLM gateway configuration", clientType)
	}

	path := cm.buildLLMSettingsPath(appCfg)

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("creating directory for %s: %w", path, err)
	}

	err := fileutils.WithFileLock(path, func() error {
		content, err := readOrInit(path)
		if err != nil {
			return err
		}

		// Parse with hujson first so that JSONC (comments, trailing commas) is
		// handled correctly for all subsequent operations.
		v, err := hujson.Parse(content)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}

		// Ensure every intermediate ancestor object exists before patching.
		// e.g. "/a/b/c" requires "/a" and "/a/b" to already be present.
		for _, spec := range appCfg.LLMGatewayKeys {
			if err := ensureLLMAncestors(&v, spec.JSONPointer, path); err != nil {
				return err
			}
		}

		for _, spec := range appCfg.LLMGatewayKeys {
			value := llmValueForSpec(spec.ValueField, cfg)
			valueJSON, err := json.Marshal(value)
			if err != nil {
				return fmt.Errorf("marshaling value for %s: %w", spec.JSONPointer, err)
			}
			patchDoc, err := json.Marshal([]llmPatchOp{{Op: "add", Path: spec.JSONPointer, Value: valueJSON}})
			if err != nil {
				return fmt.Errorf("marshaling patch for %s: %w", spec.JSONPointer, err)
			}
			if err := v.Patch(patchDoc); err != nil {
				return fmt.Errorf("patching %s in %s: %w", spec.JSONPointer, path, err)
			}
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
	return path, nil
}

// RevertLLMGateway removes the LLM gateway keys from the tool's settings file.
// If the file does not exist the call is a no-op. Comments and trailing commas
// in JSONC settings files are preserved.
func (cm *ClientManager) RevertLLMGateway(clientType ClientApp, configPath string) error {
	appCfg := cm.lookupClientAppConfig(clientType)
	if appCfg == nil || appCfg.LLMGatewayMode == "" {
		return fmt.Errorf("client %q does not support LLM gateway configuration", clientType)
	}

	// Guard against a missing file (or deleted parent directory) before trying
	// to acquire the lock — WithFileLock creates configPath+".lock", which
	// fails when the directory no longer exists.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil
	}

	return fileutils.WithFileLock(configPath, func() error {
		content, err := os.ReadFile(configPath) // #nosec G304 -- path is caller-supplied config file
		if err != nil {
			if os.IsNotExist(err) {
				return nil // file removed between stat and lock acquisition
			}
			return fmt.Errorf("reading %s: %w", configPath, err)
		}
		if len(content) == 0 {
			return nil
		}

		v, err := hujson.Parse(content)
		if err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}

		// Standardize once for all existence checks below.
		standardized, err := hujson.Standardize(v.Pack())
		if err != nil {
			return fmt.Errorf("standardizing %s: %w", configPath, err)
		}

		for _, spec := range appCfg.LLMGatewayKeys {
			// Skip keys that are already absent — avoids brittle error-string matching.
			if !jsonPointerExists(standardized, spec.JSONPointer) {
				continue
			}
			patchDoc, err := json.Marshal([]llmPatchOp{{Op: "remove", Path: spec.JSONPointer}})
			if err != nil {
				return fmt.Errorf("marshaling patch for %s: %w", spec.JSONPointer, err)
			}
			if err := v.Patch(patchDoc); err != nil {
				return fmt.Errorf("reverting %s from %s: %w", spec.JSONPointer, configPath, err)
			}
		}

		formatted, err := hujson.Format(v.Pack())
		if err != nil {
			return fmt.Errorf("formatting %s: %w", configPath, err)
		}
		return fileutils.AtomicWriteFile(configPath, formatted, 0o600)
	})
}

// IsLLMGatewaySupported reports whether clientType has LLM gateway support.
func (cm *ClientManager) IsLLMGatewaySupported(clientType ClientApp) bool {
	cfg := cm.lookupClientAppConfig(clientType)
	return cfg != nil && cfg.LLMGatewayMode != ""
}

// LLMGatewayModeFor returns "direct", "proxy", or "" for the given client.
func (cm *ClientManager) LLMGatewayModeFor(clientType ClientApp) string {
	cfg := cm.lookupClientAppConfig(clientType)
	if cfg == nil {
		return ""
	}
	return cfg.LLMGatewayMode
}

// DetectedLLMGatewayClients returns the subset of LLM-gateway-capable clients
// whose settings directory exists on this machine.
func (cm *ClientManager) DetectedLLMGatewayClients() []ClientApp {
	var result []ClientApp
	for i := range cm.clientIntegrations {
		cfg := &cm.clientIntegrations[i]
		if cfg.LLMGatewayMode == "" {
			continue
		}
		path := cm.buildLLMSettingsPath(cfg)
		if _, err := os.Stat(filepath.Dir(path)); err == nil {
			result = append(result, cfg.ClientType)
		}
	}
	return result
}

// buildLLMSettingsPath resolves the absolute path to the LLM settings file
// for the given client using the same PlatformPrefix logic as MCP config paths.
func (cm *ClientManager) buildLLMSettingsPath(cfg *clientAppConfig) string {
	return buildConfigFilePath(
		cfg.LLMSettingsFile,
		cfg.LLMSettingsRelPath,
		cfg.LLMSettingsPlatformPrefix,
		[]string{cm.homeDir},
	)
}

// llmValueForSpec returns the config value corresponding to the ValueField name.
func llmValueForSpec(valueField string, cfg LLMApplyConfig) string {
	switch valueField {
	case "GatewayURL":
		return cfg.GatewayURL
	case "ProxyBaseURL":
		return cfg.ProxyBaseURL
	case "TokenHelperCommand":
		return cfg.TokenHelperCommand
	case "PlaceholderAPIKey":
		return llmPlaceholderAPIKey
	default:
		return valueField // treat unknown field names as literal values
	}
}

// ensureLLMAncestors walks every ancestor of ptr from root inward and creates
// any missing intermediate object. For example, for "/a/b/c" it ensures "/a"
// and then "/a/b" exist, so the final "add" patch for "/a/b/c" never fails
// because a parent is missing.
//
// Existence is checked against standardized JSON (hujson.Standardize strips
// JSONC comments and trailing commas) so that JSONC input never produces a
// false "missing" result that would cause an RFC 6902 "add" to replace an
// existing object.
func ensureLLMAncestors(v *hujson.Value, ptr, filePath string) error {
	segments := strings.Split(strings.TrimPrefix(ptr, "/"), "/")
	if len(segments) <= 1 {
		return nil // top-level key — no ancestors to create
	}
	// Standardize once for all existence checks in this call.
	standardized, err := hujson.Standardize(v.Pack())
	if err != nil {
		return fmt.Errorf("standardizing JSON in %s: %w", filePath, err)
	}

	ancestor := ""
	needsCreate := false
	for _, seg := range segments[:len(segments)-1] {
		ancestor += "/" + seg
		// Once a missing ancestor is found, all deeper paths are also absent
		// (we just created an empty object), so skip further existence checks.
		if !needsCreate && jsonPointerExists(standardized, ancestor) {
			continue
		}
		needsCreate = true
		patchDoc, err := json.Marshal([]llmPatchOp{{Op: "add", Path: ancestor, Value: json.RawMessage("{}")}})
		if err != nil {
			return fmt.Errorf("marshaling ancestor patch for %s in %s: %w", ancestor, filePath, err)
		}
		if err := v.Patch(patchDoc); err != nil {
			return fmt.Errorf("creating ancestor object %s in %s: %w", ancestor, filePath, err)
		}
	}
	return nil
}

// jsonPointerExists reports whether the JSON Pointer path resolves to a value
// in standard (non-JSONC) JSON data.
// data must already be standardized via hujson.Standardize.
func jsonPointerExists(data []byte, pointer string) bool {
	var root any
	if err := json.Unmarshal(data, &root); err != nil {
		return false
	}
	current := root
	for _, seg := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		m, ok := current.(map[string]any)
		if !ok {
			return false
		}
		current, ok = m[seg]
		if !ok {
			return false
		}
	}
	return true
}

// readOrInit reads path and returns its content, or "{}" if the file is missing
// or empty. Returns an error only for real IO failures.
func readOrInit(path string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is a known tool config file location
	if err != nil {
		if os.IsNotExist(err) {
			return []byte("{}"), nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if len(data) == 0 {
		return []byte("{}"), nil
	}
	return data, nil
}
