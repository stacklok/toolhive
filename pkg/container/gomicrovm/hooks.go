// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stacklok/go-microvm"
	"github.com/stacklok/go-microvm/hooks"
	"github.com/stacklok/go-microvm/image"

	"github.com/stacklok/toolhive/pkg/container/gomicrovm/initbin"
)

// entrypointConfig mirrors the struct in cmd/thv-vm-init/entrypoint.go.
// It captures the original OCI command so the init binary can exec it.
type entrypointConfig struct {
	Cmd        []string `json:"cmd"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`
}

// InjectInitBinary returns a RootFS hook that writes the embedded thv-vm-init
// binary to /thv-vm-init in the guest rootfs.
func InjectInitBinary() microvm.RootFSHook {
	return func(rootfsPath string, _ *image.OCIConfig) error {
		initPath := filepath.Join(rootfsPath, "thv-vm-init")
		if err := os.WriteFile(initPath, initbin.Binary, 0o755); err != nil { //nolint:gosec // needs to be executable in the guest
			return fmt.Errorf("writing init binary: %w", err)
		}
		return nil
	}
}

// InjectEntrypoint returns a RootFS hook that captures the original OCI
// entrypoint and cmd into /etc/thv-entrypoint.json before WithInitOverride
// replaces it. This allows thv-vm-init to start the MCP server with the
// correct command, environment, and working directory.
func InjectEntrypoint() microvm.RootFSHook {
	return func(rootfsPath string, imgConfig *image.OCIConfig) error {
		cmd := buildCmd(imgConfig)
		if len(cmd) == 0 {
			return fmt.Errorf("OCI image has no entrypoint or cmd")
		}

		cfg := entrypointConfig{
			Cmd: cmd,
		}
		if imgConfig != nil {
			cfg.Env = imgConfig.Env
			cfg.WorkingDir = imgConfig.WorkingDir
		}

		return writeEntrypointConfig(rootfsPath, &cfg)
	}
}

// buildCmd constructs the full command from OCI entrypoint and cmd,
// following the OCI image spec: entrypoint + cmd.
func buildCmd(imgConfig *image.OCIConfig) []string {
	if imgConfig == nil {
		return nil
	}
	// OCI spec: final command = Entrypoint + Cmd
	cmd := make([]string, 0, len(imgConfig.Entrypoint)+len(imgConfig.Cmd))
	cmd = append(cmd, imgConfig.Entrypoint...)
	cmd = append(cmd, imgConfig.Cmd...)
	return cmd
}

// InjectEntrypointOverride returns a RootFS hook that uses the caller's
// command as a CMD override while preserving the OCI image's ENTRYPOINT.
// This matches Docker/Podman behavior: user args replace CMD, not ENTRYPOINT.
// The final command is ENTRYPOINT + override.
func InjectEntrypointOverride(cmd []string) microvm.RootFSHook {
	return func(rootfsPath string, imgConfig *image.OCIConfig) error {
		if len(cmd) == 0 {
			return fmt.Errorf("entrypoint override has empty cmd")
		}
		// Prepend OCI ENTRYPOINT (if any) to match Docker semantics:
		//   docker run image arg1 arg2  =>  ENTRYPOINT + [arg1, arg2]
		fullCmd := cmd
		if imgConfig != nil && len(imgConfig.Entrypoint) > 0 {
			fullCmd = make([]string, 0, len(imgConfig.Entrypoint)+len(cmd))
			fullCmd = append(fullCmd, imgConfig.Entrypoint...)
			fullCmd = append(fullCmd, cmd...)
		}
		cfg := entrypointConfig{
			Cmd: fullCmd,
		}
		if imgConfig != nil {
			cfg.Env = imgConfig.Env
			cfg.WorkingDir = imgConfig.WorkingDir
		}
		return writeEntrypointConfig(rootfsPath, &cfg)
	}
}

// writeEntrypointConfig marshals and writes the entrypoint config to the rootfs.
func writeEntrypointConfig(rootfsPath string, cfg *entrypointConfig) error {
	data, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling entrypoint config: %w", err)
	}

	epPath := filepath.Join(rootfsPath, "etc", "thv-entrypoint.json")
	if err := os.MkdirAll(filepath.Dir(epPath), 0o750); err != nil { //nolint:gosec // creating /etc in guest rootfs
		return fmt.Errorf("creating /etc in rootfs: %w", err)
	}
	if err := os.WriteFile(epPath, data, 0o644); err != nil { //nolint:gosec // config file in guest rootfs
		return fmt.Errorf("writing entrypoint config: %w", err)
	}
	return nil
}

// InjectSSHKeys returns a RootFS hook that injects SSH authorized keys for
// the root user. This satisfies boot.Run()'s requirement for an SSH server
// and provides optional debugging access.
func InjectSSHKeys(pubKey string) microvm.RootFSHook {
	return hooks.InjectAuthorizedKeys(pubKey, hooks.WithKeyUser("/root", 0, 0))
}
