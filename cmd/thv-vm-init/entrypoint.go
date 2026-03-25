// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// DefaultEntrypointPath is the guest path where the OCI entrypoint config is
// injected by the InjectEntrypoint rootfs hook.
const DefaultEntrypointPath = "/etc/thv-entrypoint.json"

// entrypointConfig holds the original OCI command, environment, and working
// directory captured from the container image before the init override replaced
// the default entrypoint.
type entrypointConfig struct {
	Cmd        []string `json:"cmd"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

// loadEntrypoint reads and parses an entrypoint config file.
func loadEntrypoint(path string) (*entrypointConfig, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a trusted guest-internal config path
	if err != nil {
		return nil, fmt.Errorf("reading entrypoint config: %w", err)
	}
	var cfg entrypointConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing entrypoint config: %w", err)
	}
	if len(cfg.Cmd) == 0 {
		return nil, fmt.Errorf("entrypoint config has empty cmd")
	}
	return &cfg, nil
}
