// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
	"github.com/stacklok/toolhive/pkg/ignore"
)

// readOnlyScratchPaths are the writable tmpfs paths added when ReadOnlyRootfs is enabled,
// matching ToolHive's Docker/Podman read-only rootfs defaults.
var readOnlyScratchPaths = []string{"/tmp", "/var/tmp", "/run"}

const readOnlyScratchTmpfsOptions = "rw,nosuid,nodev,noexec,size=64m"

func getPermissionConfigFromProfile(
	profile *permissions.Profile,
	transportType string,
	ignoreConfig *ignore.Config,
) (*runtime.PermissionConfig, error) {
	switch transportType {
	case "sse", "stdio", "inspector", "streamable-http":
		// valid
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", transportType)
	}

	networkMode := ""
	if profile != nil && profile.Network != nil {
		networkMode = strings.TrimSpace(profile.Network.Mode)
	}

	if strings.EqualFold(networkMode, "host") {
		return nil, fmt.Errorf(
			"network mode %q is not supported by the Apple container runtime because containers run in isolated Linux VMs",
			networkMode,
		)
	}

	if profile != nil && profile.Privileged {
		return nil, fmt.Errorf("privileged mode is not supported by the Apple container runtime")
	}

	config := &runtime.PermissionConfig{
		Mounts:      []runtime.Mount{},
		NetworkMode: networkMode,
		CapDrop:     []string{"ALL"},
		CapAdd:      []string{},
	}

	if profile != nil {
		addReadOnlyMounts(config, profile.Read, ignoreConfig)
		addReadWriteMounts(config, profile.Write, ignoreConfig)
	}

	if err := validateBindMounts(config.Mounts); err != nil {
		return nil, err
	}

	return config, nil
}

func addReadOnlyMounts(
	config *runtime.PermissionConfig,
	mounts []permissions.MountDeclaration,
	ignoreConfig *ignore.Config,
) {
	for _, mountDecl := range mounts {
		source, target, err := mountDecl.Parse()
		if err != nil {
			slog.Warn("skipping invalid mount declaration", "mount", mountDecl, "error", err)
			continue
		}
		if strings.Contains(source, "://") {
			slog.Warn("resource URI mounts not yet supported", "source", source)
			continue
		}

		absPath, ok := convertRelativePathToAbsolute(source, mountDecl)
		if !ok {
			continue
		}

		config.Mounts = append(config.Mounts, runtime.Mount{
			Source:   absPath,
			Target:   target,
			ReadOnly: true,
			Type:     runtime.MountTypeBind,
		})

		addIgnoreOverlays(config, absPath, target, ignoreConfig)
	}
}

func addReadWriteMounts(
	config *runtime.PermissionConfig,
	mounts []permissions.MountDeclaration,
	ignoreConfig *ignore.Config,
) {
	for _, mountDecl := range mounts {
		source, target, err := mountDecl.Parse()
		if err != nil {
			slog.Warn("skipping invalid mount declaration", "mount", mountDecl, "error", err)
			continue
		}
		if strings.Contains(source, "://") {
			slog.Warn("resource URI mounts not yet supported", "source", source)
			continue
		}

		absPath, ok := convertRelativePathToAbsolute(source, mountDecl)
		if !ok {
			continue
		}

		alreadyMounted := false
		for idx, existing := range config.Mounts {
			if existing.Target == target {
				config.Mounts[idx].ReadOnly = false
				alreadyMounted = true
				break
			}
		}

		if !alreadyMounted {
			config.Mounts = append(config.Mounts, runtime.Mount{
				Source:   absPath,
				Target:   target,
				ReadOnly: false,
				Type:     runtime.MountTypeBind,
			})
		}

		addIgnoreOverlays(config, absPath, target, ignoreConfig)
	}
}

func addIgnoreOverlays(
	config *runtime.PermissionConfig,
	sourceDir, containerPath string,
	ignoreConfig *ignore.Config,
) {
	if ignoreConfig == nil {
		return
	}

	ignoreProcessor := ignore.NewProcessor(ignoreConfig)

	if ignoreConfig.LoadGlobal {
		if err := ignoreProcessor.LoadGlobal(); err != nil {
			slog.Debug("failed to load global ignore patterns", "error", err)
		}
	}

	if err := ignoreProcessor.LoadLocal(sourceDir); err != nil {
		slog.Debug("failed to load local ignore patterns", "dir", sourceDir, "error", err)
	}

	overlayMounts := ignoreProcessor.GetOverlayMounts(sourceDir, containerPath)
	for _, overlayMount := range overlayMounts {
		config.Mounts = append(config.Mounts, runtime.Mount{
			Source:   overlayMount.HostPath,
			Target:   overlayMount.ContainerPath,
			ReadOnly: false,
			Type:     runtime.MountTypeBind,
		})
		slog.Debug("added bind overlay for ignored path",
			"host_path", overlayMount.HostPath,
			"container_path", overlayMount.ContainerPath)
	}
}

func convertRelativePathToAbsolute(source string, mountDecl permissions.MountDeclaration) (string, bool) {
	if filepath.IsAbs(source) {
		return source, true
	}

	absPath, err := filepath.Abs(source)
	if err != nil {
		slog.Warn("failed to convert to absolute path", "mount", mountDecl, "error", err)
		return "", false
	}

	slog.Debug("converting relative path to absolute", "mount", mountDecl, "abs_path", absPath)
	return absPath, true
}

func validateBindMounts(mounts []runtime.Mount) error {
	for _, mnt := range mounts {
		if mnt.Type == runtime.MountTypeTmpfs {
			continue
		}
		if strings.Contains(mnt.Source, ":") || strings.Contains(mnt.Target, ":") {
			return fmt.Errorf(
				"bind mount path %q -> %q contains ':', which is not supported by Apple container --volume",
				mnt.Source, mnt.Target,
			)
		}
	}
	return nil
}

func appendMountArgs(args []string, permConfig *runtime.PermissionConfig) ([]string, error) {
	if permConfig == nil {
		return args, nil
	}
	if err := validateBindMounts(permConfig.Mounts); err != nil {
		return nil, err
	}

	mountedTargets := make(map[string]bool, len(permConfig.Mounts))
	for _, mnt := range permConfig.Mounts {
		mountedTargets[mnt.Target] = true
		if mnt.Type == runtime.MountTypeTmpfs {
			args = append(args, "--tmpfs", mnt.Target)
			continue
		}

		// Use --volume rather than --mount for user bind mounts and .thvignore
		// overlays because apple/container's --volume accepts both files and
		// directories, whereas --mount rejects non-directory host paths.
		volSpec := fmt.Sprintf("%s:%s", mnt.Source, mnt.Target)
		if mnt.ReadOnly {
			volSpec += ":ro"
		}
		args = append(args, "--volume", volSpec)
	}

	if permConfig.ReadOnlyRootfs {
		args = append(args, "--read-only")
		for _, scratchPath := range readOnlyScratchPaths {
			if !mountedTargets[scratchPath] {
				args = append(args, "--tmpfs", fmt.Sprintf("%s:%s", scratchPath, readOnlyScratchTmpfsOptions))
			}
		}
	}

	return args, nil
}

func appendSecurityArgs(args []string, permConfig *runtime.PermissionConfig) ([]string, error) {
	if permConfig == nil {
		return append(args, "--cap-drop", "ALL"), nil
	}

	if permConfig.Privileged {
		return nil, fmt.Errorf("privileged mode is not supported by the Apple container runtime")
	}

	for _, capName := range permConfig.CapDrop {
		args = append(args, "--cap-drop", capName)
	}
	for _, capName := range permConfig.CapAdd {
		args = append(args, "--cap-add", capName)
	}
	return args, nil
}
