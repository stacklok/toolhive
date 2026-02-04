// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/stacklok/toolhive/test/e2e"
)

// sharedConfigDir is created once for the entire test suite
// All API server subprocesses will use this same directory
var sharedConfigDir string

func TestE2e(t *testing.T) { //nolint:paralleltest // E2E tests should not run in parallel
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2e Suite")
}

var _ = BeforeSuite(func() {
	// Create a shared config directory for all API tests
	// This ensures all thv serve subprocesses see the same workload state
	var err error
	sharedConfigDir, err = os.MkdirTemp("", "toolhive-e2e-shared-*")
	Expect(err).ToNot(HaveOccurred())

	// Set environment variable so api_helpers.go can use it
	os.Setenv("TOOLHIVE_E2E_SHARED_CONFIG", sharedConfigDir)
})

var _ = AfterSuite(func() {
	// Clean up the shared config directory
	// This is safe because it's an isolated temp directory created by BeforeSuite
	if sharedConfigDir != "" {
		// Clean up any remaining test workloads by name (surgical approach)
		cleanupTestWorkloadsByName()

		// Remove the entire temp directory
		GinkgoWriter.Printf("Removing test config directory: %s\n", sharedConfigDir)
		if err := os.RemoveAll(sharedConfigDir); err != nil {
			GinkgoWriter.Printf("Warning: Failed to remove test config directory: %v\n", err)
		}
		GinkgoWriter.Printf("Test cleanup complete\n")
	}
})

// cleanupTestWorkloadsByName discovers test workloads from the isolated config directory
// and deletes them specifically by name. This is safe because:
// 1. We only delete workloads that exist in the isolated test config
// 2. We delete them by explicit name (not --all)
// 3. Real workloads are unaffected because they're not in the test config
func cleanupTestWorkloadsByName() {
	testConfig := e2e.NewTestConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	// SAFETY CHECK: Verify we're using a temp directory
	if !strings.Contains(sharedConfigDir, "toolhive-e2e-shared-") {
		GinkgoWriter.Printf("ERROR: Config directory does not look like a test directory: %s\n", sharedConfigDir)
		GinkgoWriter.Printf("Skipping cleanup to avoid affecting real workloads!\n")
		return
	}

	// List workloads from the isolated test config
	workloadNames := listTestWorkloadNames()
	if len(workloadNames) == 0 {
		GinkgoWriter.Printf("No test workloads found to clean up\n")
		return
	}

	GinkgoWriter.Printf("Cleaning up %d test workload(s): %v\n", len(workloadNames), workloadNames)

	// Set up environment to use the ISOLATED test config directory
	env := []string{
		"XDG_CONFIG_HOME=" + sharedConfigDir,
		"XDG_DATA_HOME=" + sharedConfigDir,
		"HOME=" + sharedConfigDir,
		"TOOLHIVE_DEV=true",
	}

	// Delete each test workload specifically by name
	for _, name := range workloadNames {
		//nolint:gosec // Intentional for cleanup with specific workload names
		rmCmd := exec.CommandContext(ctx, testConfig.THVBinary, "rm", name)
		rmCmd.Env = env
		if err := rmCmd.Run(); err != nil {
			GinkgoWriter.Printf("Warning: Failed to delete test workload %s: %v\n", name, err)
		}
	}

	GinkgoWriter.Printf("Test workload cleanup complete\n")
}

// listTestWorkloadNames reads the isolated test config directory to find workload names.
// It checks both run configs and status files to catch all workloads.
func listTestWorkloadNames() []string {
	namesMap := make(map[string]bool)

	// Check run configs: XDG_DATA_HOME/toolhive/run_configs/
	runConfigsDir := filepath.Join(sharedConfigDir, "toolhive", "run_configs")
	if entries, err := os.ReadDir(runConfigsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// Run config files are named <workload-name>.json
			name := strings.TrimSuffix(entry.Name(), ".json")
			if name != "" && name != entry.Name() { // Valid JSON file
				namesMap[name] = true
			}
		}
	}

	// Check status files: XDG_DATA_HOME/toolhive/statuses/
	// This catches workloads that have orphaned containers but lost their run configs
	statusesDir := filepath.Join(sharedConfigDir, "toolhive", "statuses")
	if entries, err := os.ReadDir(statusesDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			// Status files are named <workload-name>.json
			name := strings.TrimSuffix(entry.Name(), ".json")
			if name != "" && name != entry.Name() { // Valid JSON file
				namesMap[name] = true
			}
		}
	}

	// Convert map to slice
	names := make([]string, 0, len(namesMap))
	for name := range namesMap {
		names = append(names, name)
	}

	return names
}
