// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tools

// Adapter is the interface all tool adapters must implement.
// Implementing this interface and registering via Register() is all that is
// required to add a new tool — no changes to setup/teardown orchestration.
type Adapter interface {
	// Name returns the canonical tool identifier (e.g. "claude-code").
	Name() string
	// Mode returns the authentication mode: "direct" (token helper) or "proxy".
	Mode() string
	// Detect reports whether the tool is installed on this machine.
	Detect() bool
	// Apply configures the tool to use the LLM gateway.
	// It returns the absolute path to the config file that was patched.
	Apply(cfg ApplyConfig) (configPath string, err error)
	// Revert undoes the configuration changes made by Apply.
	// configPath is the path returned by Apply, stored in llm.ToolConfig.
	Revert(configPath string) error
}

// Mode constants for the two supported authentication modes.
const (
	ModeDirect = "direct" // token helper: tool invokes thv llm token
	ModeProxy  = "proxy"  // proxy mode: tool talks to the localhost reverse proxy
)

// PlaceholderAPIKey is the static placeholder inserted into proxy-mode tool
// configs. The localhost proxy strips this and injects a real OIDC token.
const PlaceholderAPIKey = "thv-proxy"

// ApplyConfig carries the parameters an Adapter needs to configure a tool.
type ApplyConfig struct {
	// GatewayURL is the upstream LLM gateway URL (used by token-helper tools).
	GatewayURL string
	// ProxyBaseURL is the localhost proxy base URL (used by proxy-mode tools).
	// Typically "http://localhost:<port>/v1".
	ProxyBaseURL string
	// TokenHelperCommand is the full shell command that prints a fresh OIDC
	// access token to stdout. Typically "<abs-path-to-thv> llm token".
	TokenHelperCommand string
}

// applyPatch is the shared Apply body used by every adapter: resolve the
// settings path, apply patchFn to the JSON file, and return the path.
func applyPatch(pathFn func() (string, error), patchFn func(map[string]any)) (string, error) {
	path, err := pathFn()
	if err != nil {
		return "", err
	}
	if err := patchJSONFile(path, patchFn); err != nil {
		return "", err
	}
	return path, nil
}
