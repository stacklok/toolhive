// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package client

// LLMTestEntry describes a minimal LLM-gateway-capable client integration for
// use in tests. No platform prefix is applied, so settings files resolve as
// homeDir/SettingsDir.../SettingsFile on all platforms.
type LLMTestEntry struct {
	ClientType   ClientApp
	Mode         string   // "direct" or "proxy"
	SettingsDir  []string // path segments from homeDir to the settings directory
	SettingsFile string   // settings filename
	JSONPointers []string // RFC 6901 JSON Pointer paths to patch
	ValueFields  []string // value-field names parallel to JSONPointers
}

// LLMTestIntegrations converts []LLMTestEntry into an internal []clientAppConfig
// slice suitable for passing as the third argument to NewTestClientManager.
// Because clientAppConfig is unexported, callers assign the result via :=
// (type inferred) and pass it directly to NewTestClientManager.
func LLMTestIntegrations(entries []LLMTestEntry) []clientAppConfig {
	cfgs := make([]clientAppConfig, len(entries))
	for i, e := range entries {
		keys := make([]LLMGatewayKeySpec, len(e.JSONPointers))
		for j, ptr := range e.JSONPointers {
			vf := ""
			if j < len(e.ValueFields) {
				vf = e.ValueFields[j]
			}
			keys[j] = LLMGatewayKeySpec{JSONPointer: ptr, ValueField: vf}
		}
		cfgs[i] = clientAppConfig{
			ClientType:         e.ClientType,
			LLMGatewayMode:     e.Mode,
			LLMSettingsFile:    e.SettingsFile,
			LLMSettingsRelPath: e.SettingsDir,
			LLMGatewayKeys:     keys,
		}
	}
	return cfgs
}
