// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package process

import (
	"slices"
	"strings"

	gopsutilprocess "github.com/shirou/gopsutil/v4/process"
)

const (
	// toolHiveBinaryName is the binary name used to identify ToolHive processes.
	// Used when TOOLHIVE_DETACHED cannot be read (e.g. platform restrictions).
	toolHiveBinaryName = "thv"
)

//go:generate mockgen -destination=mocks/mock_process_metadata.go -package=mocks -source=toolhive_proxy.go -mock_names=processMetadata=MockProcessMetadata

type processMetadata interface {
	Environ() ([]string, error)
	Cmdline() (string, error)
	CmdlineSlice() ([]string, error)
	Exe() (string, error)
}

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

// IsTrackedToolHiveProxyForWorkload verifies a PID read from a workload's status.
// Detached supervisors must match the workload name. Non-detached ToolHive
// run/start/restart supervisors use recorded-PID binding without a process-birth-time check.
// Only the subcommand and its ToolHive arguments before "--" are checked.
// An unnamed or --from-config run may omit its derived workload name. Named
// runs must match; start/restart must include the name or select --all or --group.
// Port-discovered PIDs must use IsToolHiveProxyForWorkload instead.
func IsTrackedToolHiveProxyForWorkload(pid int, workloadName string) (bool, error) {
	if pid <= 0 || workloadName == "" {
		return false, nil
	}
	p, err := gopsutilprocess.NewProcess(int32(pid)) //nolint:gosec // G115: supported platform PIDs fit int32.
	if err != nil {
		return false, err
	}
	return isTrackedToolHiveProxyForWorkload(p, workloadName)
}

func isTrackedToolHiveProxyForWorkload(p processMetadata, workloadName string) (bool, error) {
	env, err := p.Environ()
	if err != nil {
		return false, err
	}
	if slices.Contains(env, ToolHiveDetachedEnv+"="+ToolHiveDetachedValue) {
		cmdline, err := p.Cmdline()
		if err != nil {
			return false, err
		}
		return cmdlineContainsWorkload(cmdline, workloadName), nil
	}
	args, err := p.CmdlineSlice()
	if err != nil {
		return false, err
	}
	if !isToolHiveProcess(p) {
		return false, nil
	}
	switch command, rest := supervisorSubcommand(args); command {
	case "run":
		return trackedRunMatchesWorkload(rest, workloadName), nil
	case "start", "restart":
		return trackedStartMatchesWorkload(rest, workloadName), nil
	}
	return false, nil
}

// IsForegroundRunSupervisorForWorkload reports whether pid is a non-detached
// `thv run` supervising workloadName. Such a process deletes its workload when
// it exits, so it cannot be replaced by another supervisor.
func IsForegroundRunSupervisorForWorkload(pid int, workloadName string) (bool, error) {
	if pid <= 0 || workloadName == "" {
		return false, nil
	}
	p, err := gopsutilprocess.NewProcess(int32(pid)) //nolint:gosec // G115: supported platform PIDs fit int32.
	if err != nil {
		return false, err
	}
	return isForegroundRunSupervisorForWorkload(p, workloadName)
}

func isForegroundRunSupervisorForWorkload(p processMetadata, workloadName string) (bool, error) {
	env, err := p.Environ()
	if err != nil {
		return false, err
	}
	if slices.Contains(env, ToolHiveDetachedEnv+"="+ToolHiveDetachedValue) {
		return false, nil
	}
	args, err := p.CmdlineSlice()
	if err != nil {
		return false, err
	}
	if !isToolHiveProcess(p) {
		return false, nil
	}
	command, rest := supervisorSubcommand(args)
	return command == "run" && trackedRunMatchesWorkload(rest, workloadName), nil
}

// supervisorSubcommand returns the first non-flag argument after argv[0] and
// the arguments that follow it, up to "--". The only root flag, --debug, takes
// no value, so the first non-flag argument is the subcommand. Arguments after
// "--" belong to the MCP server, not ToolHive.
func supervisorSubcommand(argv []string) (string, []string) {
	for i := 1; i < len(argv); i++ {
		if argv[i] == "--" {
			return "", nil
		}
		if strings.HasPrefix(argv[i], "-") {
			continue
		}
		rest := argv[i+1:]
		if end := slices.Index(rest, "--"); end >= 0 {
			rest = rest[:end]
		}
		return argv[i], rest
	}
	return "", nil
}

// trackedStartMatchesWorkload accepts start/restart arguments that name the
// workload or select it through --all/-a or --group/-g.
func trackedStartMatchesWorkload(args []string, workloadName string) bool {
	return slices.ContainsFunc(args, func(arg string) bool {
		switch {
		case arg == workloadName, arg == "--all", arg == "--group", strings.HasPrefix(arg, "--group="):
			return true
		case strings.HasPrefix(arg, "--all="):
			return strings.TrimPrefix(arg, "--all=") != "false"
		case strings.HasPrefix(arg, "--"):
			return false
		case strings.HasPrefix(arg, "-"):
			// Shorthand cluster such as -a, -af, -g, or -g=name.
			shorthands, _, _ := strings.Cut(arg[1:], "=")
			return strings.ContainsAny(shorthands, "ag")
		}
		return false
	})
}

func trackedRunMatchesWorkload(args []string, workloadName string) bool {
	named := false
	fromConfig := false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--name":
			named = true
			i++
			if i >= len(args) || args[i] != workloadName {
				return false
			}
		case strings.HasPrefix(args[i], "--name="):
			named = true
			if strings.TrimPrefix(args[i], "--name=") != workloadName {
				return false
			}
		case args[i] == "--from-config", strings.HasPrefix(args[i], "--from-config="):
			// The workload name comes from the config file, like an unnamed run.
			fromConfig = true
		}
	}
	return named || fromConfig || slices.ContainsFunc(args, func(arg string) bool {
		return !strings.HasPrefix(arg, "-")
	})
}

// isToolHiveProcess returns true if p is a ToolHive process (TOOLHIVE_DETACHED
// or thv binary in exe/cmdline).
func isToolHiveProcess(p processMetadata) bool {
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

func hasToolHiveDetachedEnv(p processMetadata) bool {
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
	// A hyphen continues the workload name; it is not a token boundary.
	idx := strings.Index(cmdline, pattern)
	end := idx + len(pattern)
	if end >= len(cmdline) {
		return true
	}
	next := cmdline[end]
	return next == ' '
}
