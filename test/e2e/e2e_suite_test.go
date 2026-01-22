// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
	// Clean up the shared config directory after all tests complete
	if sharedConfigDir != "" {
		_ = os.RemoveAll(sharedConfigDir)
	}
})
