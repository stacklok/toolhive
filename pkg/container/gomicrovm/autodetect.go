// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package gomicrovm

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/stacklok/toolhive/pkg/container/gomicrovm/runtimebin"
)

// IsAvailable checks whether the go-microvm microVM runtime can be used.
// It requires Linux with KVM access and either an embedded runtime or the
// go-microvm-runner binary on disk.
func IsAvailable() bool {
	if !kvmAccessible() {
		slog.Debug("go-microvm: /dev/kvm is not accessible")
		return false
	}
	if runtimebin.Available() {
		slog.Debug("[EXPERIMENTAL] go-microvm microVM runtime detected as available (embedded runtime)")
		return true
	}
	if _, err := FindRunnerPath(); err != nil {
		slog.Debug("go-microvm: runner binary not found", "error", err)
		return false
	}
	slog.Debug("[EXPERIMENTAL] go-microvm microVM runtime detected as available")
	return true
}

// kvmAccessible checks that /dev/kvm exists and is writable.
func kvmAccessible() bool {
	return unix.Access("/dev/kvm", unix.W_OK) == nil
}

// FindRunnerPath locates the go-microvm-runner binary and returns its absolute path.
// The lookup order mirrors the go-microvm library's own runner finder:
//  1. GO_MICROVM_RUNNER_PATH environment variable
//  2. System PATH
//  3. Next to the current executable (e.g., bin/go-microvm-runner alongside bin/thv)
func FindRunnerPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("GO_MICROVM_RUNNER_PATH")); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", fmt.Errorf("GO_MICROVM_RUNNER_PATH=%q does not exist", p)
	}
	if p, err := exec.LookPath("go-microvm-runner"); err == nil {
		return p, nil
	}
	if execPath, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(execPath), "go-microvm-runner")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("go-microvm-runner not found (set GO_MICROVM_RUNNER_PATH or place it in $PATH or next to the thv binary)")
}
