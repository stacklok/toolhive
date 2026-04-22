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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
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
	logLevel := zapcore.ErrorLevel
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true), zap.Level(logLevel)))

	ctx, cancel = context.WithCancel(context.TODO())

	By("bootstrapping envtest")
	testEnv = &envtest.Environment{
		ErrorIfCRDPathMissing: false, // tests install CRDs on demand
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	utilruntime.Must(apiextensionsv1.AddToScheme(scheme.Scheme))

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())
})

var _ = AfterSuite(func() {
	By("tearing down envtest")
	cancel()
	time.Sleep(100 * time.Millisecond)
	Expect(testEnv.Stop()).NotTo(HaveOccurred())
})
