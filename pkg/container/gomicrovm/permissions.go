// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package gomicrovm

import (
	"fmt"
	"math"
	"strings"

	"github.com/stacklok/go-microvm"

	"github.com/stacklok/toolhive-core/permissions"
	"github.com/stacklok/toolhive/pkg/container/runtime"
)

// buildEgressPolicy converts toolhive-core NetworkPermissions into a go-microvm
// EgressPolicy.  Returns nil when no restriction is needed (InsecureAllowAll,
// nil input, or empty allow list).
func buildEgressPolicy(netPerm *permissions.NetworkPermissions) *microvm.EgressPolicy {
	if netPerm == nil || netPerm.Outbound == nil {
		return nil
	}
	out := netPerm.Outbound
	if out.InsecureAllowAll {
		return nil
	}
	if len(out.AllowHost) == 0 {
		return nil
	}

	ports := convertPorts(out.AllowPort)

	hosts := make([]microvm.EgressHost, len(out.AllowHost))
	for i, h := range out.AllowHost {
		hosts[i] = microvm.EgressHost{Name: h, Ports: ports}
	}
	return &microvm.EgressPolicy{AllowedHosts: hosts}
}

// convertPorts filters integer ports to valid uint16 values (1–65535).
func convertPorts(ports []int) []uint16 {
	var out []uint16
	for _, p := range ports {
		if p > 0 && p <= math.MaxUint16 {
			out = append(out, uint16(p))
		}
	}
	return out
}

// buildVirtioFSMounts converts bind mounts from a PermissionConfig into
// go-microvm VirtioFSMount entries.  Non-bind mounts are skipped.
// The mount tag uses the original index in the Mounts slice.
func buildVirtioFSMounts(permConfig *runtime.PermissionConfig) []microvm.VirtioFSMount {
	if permConfig == nil {
		return nil
	}

	var mounts []microvm.VirtioFSMount
	for i, m := range permConfig.Mounts {
		if m.Type != runtime.MountTypeBind {
			continue
		}
		mounts = append(mounts, microvm.VirtioFSMount{
			Tag:      mountTag(i, m.Source),
			HostPath: m.Source,
		})
	}
	return mounts
}

// mountTag generates a deterministic tag for a VirtioFS mount.
func mountTag(idx int, _ string) string {
	return fmt.Sprintf("thv%d", idx)
}

// mapPermissionProfile converts a toolhive-core permissions.Profile into the
// runtime.PermissionConfig used by go-microvm for mounts, network, and privileges.
func mapPermissionProfile(profile *permissions.Profile) *runtime.PermissionConfig {
	if profile == nil {
		return nil
	}

	cfg := &runtime.PermissionConfig{}

	for _, decl := range profile.Read {
		host, guest := parseMountDecl(decl)
		cfg.Mounts = append(cfg.Mounts, runtime.Mount{
			Source:   host,
			Target:   guest,
			ReadOnly: true,
			Type:     runtime.MountTypeBind,
		})
	}
	for _, decl := range profile.Write {
		host, guest := parseMountDecl(decl)
		cfg.Mounts = append(cfg.Mounts, runtime.Mount{
			Source:   host,
			Target:   guest,
			ReadOnly: false,
			Type:     runtime.MountTypeBind,
		})
	}

	if profile.Network != nil {
		cfg.NetworkMode = profile.Network.Mode
	}
	cfg.Privileged = profile.Privileged

	// Return nil when the config is effectively empty.
	if len(cfg.Mounts) == 0 && cfg.NetworkMode == "" && !cfg.Privileged {
		return nil
	}

	return cfg
}

// parseMountDecl splits a mount declaration into host and guest paths.
// Format: "/path" (same for both) or "/host:/guest".
func parseMountDecl(decl permissions.MountDeclaration) (host, guest string) {
	s := string(decl)
	if idx := strings.Index(s, ":"); idx >= 0 {
		return s[:idx], s[idx+1:]
	}
	return s, s
}
