// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package storageversionmigrator contains envtest-backed integration tests
// for the StorageVersionMigrator controller.
//
// The suite does NOT pre-install any CRDs — each test constructs and installs
// the exact CRD it needs, which keeps scenarios independent and lets us
// exercise edge cases (foreign groups, missing status subresource, etc.) that
// wouldn't be possible with the real toolhive CRD manifests.
//
// The suite also does NOT start a controller manager. Each test constructs its
// own reconciler and calls Reconcile directly. This is more deterministic than
// manager-driven tests (no Eventually() races against the background
// controller) and lets individual tests inject custom clients to exercise
// failure paths.
package storageversionmigrator

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/stacklok/toolhive/cmd/thv-operator/test-integration/testutil"
)

var (
	k8sClient client.Client
	ctx       context.Context
	suiteEnv  *testutil.SuiteEnv
)

func TestStorageVersionMigrator(t *testing.T) {
	t.Parallel()
	RegisterFailHandler(Fail)

	suiteConfig, reporterConfig := GinkgoConfiguration()
	reporterConfig.Verbose = false
	reporterConfig.VeryVerbose = false
	reporterConfig.FullTrace = false

	RunSpecs(t, "StorageVersionMigrator Controller Integration Test Suite", suiteConfig, reporterConfig)
}

var _ = BeforeSuite(func() {
	suiteEnv = testutil.StartMinimalSuite()
	k8sClient = suiteEnv.Client
	ctx = suiteEnv.Ctx
})

var _ = AfterSuite(func() {
	suiteEnv.Stop()
})
