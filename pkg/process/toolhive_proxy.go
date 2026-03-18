// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"strings"

	gopsutilprocess "github.com/shirou/gopsutil/v4/process"
)

const (
	// toolHiveBinaryName is the binary name used to identify ToolHive processes.
	// Used when TOOLHIVE_DETACHED cannot be read (e.g. platform restrictions).
	toolHiveBinaryName = "thv"
)

// containsThvBinary returns true if s appears to reference the thv binary
// (e.g. /usr/bin/thv, thv start, thv.exe), avoiding false positives like "toolhive".
func containsThvBinary(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, toolHiveBinaryName+".exe") ||
		strings.HasSuffix(lower, toolHiveBinaryName) ||
		strings.Contains(lower, "/"+toolHiveBinaryName) ||
		strings.Contains(lower, `\`+toolHiveBinaryName) ||
		strings.HasPrefix(lower, toolHiveBinaryName+" ") ||
		strings.Contains(lower, " "+toolHiveBinaryName+" ") ||
		strings.HasSuffix(lower, " "+toolHiveBinaryName)
}

// IsToolHiveProxyForWorkload returns true if the given PID belongs to the ToolHive
// proxy for the specified workload, so it is safe to kill when freeing a port.
// Returns false if the process is not that workload's proxy or if identity cannot
// be verified (fail-safe: do not kill).
//
// When workloadName is empty, only verifies it is a ToolHive process.
// When workloadName is non-empty, also verifies the process cmdline contains
// " start <workloadName> " (the detached proxy runs "thv start <name> --foreground").
//
// Verification checks, in order:
//  1. TOOLHIVE_DETACHED=1 in process environment (most reliable)
//  2. "thv" in executable path or command line (fallback when env unavailable)
//  3. workloadName in cmdline (when provided, avoids killing another workload's proxy)
func IsToolHiveProxyForWorkload(pid int, workloadName string) (bool, error) {
	if pid <= 0 {
		return false, nil
	}

	// PID fits in int32 on all supported platforms
	p, err := gopsutilprocess.NewProcess(int32(pid)) //nolint:gosec // G115
	if err != nil {
		return false, err
	}

	if !isToolHiveProcess(p) {
		return false, nil
	}

	if workloadName != "" {
		cmdline, err := p.Cmdline()
		if err != nil || !cmdlineContainsWorkload(cmdline, workloadName) {
			return false, nil
		}
	}

	return true, nil
}

// isToolHiveProcess returns true if p is a ToolHive process (TOOLHIVE_DETACHED
// or thv binary in exe/cmdline).
func isToolHiveProcess(p *gopsutilprocess.Process) bool {
	if hasToolHiveDetachedEnv(p) {
		return true
	}
	if exe, err := p.Exe(); err == nil && containsThvBinary(exe) {
		return true
	}
	if cmdline, err := p.Cmdline(); err == nil && containsThvBinary(cmdline) {
		return true
	}
	return false
}

func hasToolHiveDetachedEnv(p *gopsutilprocess.Process) bool {
	env, err := p.Environ()
	if err != nil {
		return false
	}
	target := ToolHiveDetachedEnv + "=" + ToolHiveDetachedValue
	for _, e := range env {
		if e == target {
			return true
		}
	}
	return false
}

// cmdlineContainsWorkload returns true if the cmdline indicates a "thv start <name> ..." process.
// Uses word boundaries to avoid partial matches (e.g. "g" matching "github").
func cmdlineContainsWorkload(cmdline, workloadName string) bool {
	if workloadName == "" {
		return false
	}
	pattern := " start " + workloadName
	if !strings.Contains(cmdline, pattern) {
		return false
	}
	// Ensure workloadName is not a prefix of a longer word: next char must be space, -, or end
	idx := strings.Index(cmdline, pattern)
	end := idx + len(pattern)
	if end >= len(cmdline) {
		return true
	}
	next := cmdline[end]
	return next == ' ' || next == '-'
}
