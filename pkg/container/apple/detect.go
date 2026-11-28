// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package apple

import (
	"context"
	"fmt"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"
)

const detectionTimeout = 5 * time.Second

// IsAvailable passively checks whether the Apple macOS native container runtime
// is usable on this host. It never starts the container daemon.
func IsAvailable() bool {
	ctx, cancel := context.WithTimeout(context.Background(), detectionTimeout)
	defer cancel()

	return checkAvailability(ctx, execCommandRunner{}, goruntime.GOOS, goruntime.GOARCH) == nil
}

func checkAvailability(ctx context.Context, runner CommandRunner, goos, goarch string) error {
	if goos != "darwin" {
		return fmt.Errorf("the Apple container runtime is only supported on macOS (detected %s)", goos)
	}
	if goarch != "arm64" {
		return fmt.Errorf("the Apple container runtime requires Apple Silicon (arm64); detected %s", goarch)
	}
	if _, err := runner.LookPath(containerBinary); err != nil {
		return fmt.Errorf("the Apple container CLI (%q) not found in PATH: %w", containerBinary, err)
	}
	if err := checkVersion(ctx, runner); err != nil {
		return err
	}
	if _, err := runner.Run(ctx, containerBinary, "system", "status"); err != nil {
		return fmt.Errorf("the Apple container daemon is not running; start it with `container system start`: %w", err)
	}
	return nil
}

func checkVersion(ctx context.Context, runner CommandRunner) error {
	out, err := runner.Run(ctx, containerBinary, "--version")
	if err != nil {
		return fmt.Errorf("failed to determine Apple container CLI version: %w", err)
	}
	major, minor, patch, ok := parseVersion(string(out))
	if !ok {
		return fmt.Errorf("could not parse Apple container CLI version from output %q", strings.TrimSpace(string(out)))
	}
	if major < minContainerMajor {
		return fmt.Errorf(
			"the Apple container CLI version %d.%d.%d is unsupported; version %d.0.0 or later is required",
			major, minor, patch, minContainerMajor,
		)
	}
	return nil
}

func checkMacOSVersionForIsolation(ctx context.Context, runner CommandRunner) error {
	out, err := runner.Run(ctx, swVersBinary, "-productVersion")
	if err != nil {
		return fmt.Errorf("failed to determine macOS version for network isolation: %w", err)
	}
	trimmed := strings.TrimSpace(string(out))
	major, _, _, ok := parseVersion(trimmed)
	if !ok {
		return fmt.Errorf("could not parse macOS product version %q", trimmed)
	}
	if major < minMacOSIsolationMajor {
		return fmt.Errorf(
			"network isolation (--isolate-network) with Apple container requires macOS %d (Tahoe) or later "+
				"for custom internal vmnet networks; detected macOS %s",
			minMacOSIsolationMajor, trimmed,
		)
	}
	return nil
}

// parseVersion extracts the first dot-separated numeric version (X.Y or X.Y.Z)
// from arbitrary CLI output such as "container CLI version 1.0.2 (build 42)" or "26.1".
func parseVersion(output string) (major, minor, patch int, ok bool) {
	for _, token := range strings.Fields(output) {
		cleaned := strings.Trim(token, "(),[]vV")
		parts := strings.Split(cleaned, ".")
		if len(parts) < 2 || len(parts) > 3 {
			continue
		}
		maj, errMaj := strconv.Atoi(parts[0])
		minVal, errMin := strconv.Atoi(parts[1])
		if errMaj != nil || errMin != nil || maj < 0 || minVal < 0 {
			continue
		}
		pat := 0
		if len(parts) == 3 {
			parsedPatch, errPat := strconv.Atoi(parts[2])
			if errPat != nil || parsedPatch < 0 {
				continue
			}
			pat = parsedPatch
		}
		return maj, minVal, pat, true
	}
	return 0, 0, 0, false
}
