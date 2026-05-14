// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spectoconfig

// This file holds drift-detection tests that compare the CRD-side telemetry
// configuration type (v1beta1.MCPTelemetryConfigSpec) against its runtime
// counterpart (telemetry.Config). The goal is to fail any time a new leaf
// field is added on either side without an explicit decision about whether
// the field should be mirrored across the boundary or intentionally only
// exists on one side.
//
// The test uses three declarative tables as the source of truth:
//
//   * telemetryFieldMappings        — leaf fields present on BOTH sides,
//                                     paired by their dot-delimited paths.
//   * telemetryIgnoredOnCRDOnly     — leaf fields that only exist on the
//                                     CRD side, with a justification.
//   * telemetryIgnoredOnRuntimeOnly — leaf fields that only exist on the
//                                     runtime side, with a justification.
//
// When either side gains or loses a field, exactly one of these tables must
// be updated. testutil.AssertNoDrift handles the verification — see its
// godoc for the failure modes covered.

import (
	"testing"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

// telemetryFieldMappings is the source of truth for CRD<->runtime field
// links. One entry per leaf field that exists on both sides. The CRD path is
// rooted at MCPTelemetryConfigSpec; the runtime path is rooted at
// telemetry.Config.
var telemetryFieldMappings = []testutil.FieldMapping{
	{CRD: "openTelemetry.endpoint", Runtime: "endpoint"},
	{CRD: "openTelemetry.insecure", Runtime: "insecure"},
	{CRD: "openTelemetry.headers", Runtime: "headers"},
	{CRD: "openTelemetry.resourceAttributes", Runtime: "customAttributes"},
	{CRD: "openTelemetry.tracing.enabled", Runtime: "tracingEnabled"},
	{CRD: "openTelemetry.tracing.samplingRate", Runtime: "samplingRate"},
	{CRD: "openTelemetry.metrics.enabled", Runtime: "metricsEnabled"},
	{CRD: "openTelemetry.useLegacyAttributes", Runtime: "useLegacyAttributes"},
	{CRD: "prometheus.enabled", Runtime: "enablePrometheusMetricsPath"},
}

// telemetryIgnoredOnCRDOnly lists CRD leaf fields that intentionally have no
// runtime counterpart. Each entry MUST include a justification.
var telemetryIgnoredOnCRDOnly = map[string]string{
	"openTelemetry.enabled": "CRD-only gate; controls whether the converter populates runtime fields at all",
	"openTelemetry.sensitiveHeaders.name": "K8s-secret-backed header value; injected as TOOLHIVE_OTEL_HEADER_* env vars on the proxyrunner pod " +
		"(controllerutil.GenerateOpenTelemetryEnvVarsFromRef) and merged into OTLP headers at runtime; not written into telemetry.Config.Headers by the converter",
	"openTelemetry.sensitiveHeaders.secretKeyRef.name": "K8s-secret-backed header value; injected as TOOLHIVE_OTEL_HEADER_* env vars on the proxyrunner pod " +
		"(controllerutil.GenerateOpenTelemetryEnvVarsFromRef) and merged into OTLP headers at runtime; not written into telemetry.Config.Headers by the converter",
	"openTelemetry.sensitiveHeaders.secretKeyRef.key": "K8s-secret-backed header value; injected as TOOLHIVE_OTEL_HEADER_* env vars on the proxyrunner pod " +
		"(controllerutil.GenerateOpenTelemetryEnvVarsFromRef) and merged into OTLP headers at runtime; not written into telemetry.Config.Headers by the converter",
	"openTelemetry.caBundleRef.configMapRef.name":     "K8s ConfigMap reference; resolved by operator into runtime CACertPath",
	"openTelemetry.caBundleRef.configMapRef.key":      "K8s ConfigMap reference; resolved by operator into runtime CACertPath",
	"openTelemetry.caBundleRef.configMapRef.optional": "K8s ConfigMap reference flag promoted from corev1.ConfigMapKeySelector; not part of runtime config",
}

// telemetryIgnoredOnRuntimeOnly lists runtime leaf fields that intentionally
// have no CRD counterpart. Each entry MUST include a justification.
var telemetryIgnoredOnRuntimeOnly = map[string]string{
	"serviceName": "per-server, set from MCPTelemetryConfigReference.ServiceName and defaulted at runtime by " +
		"telemetry.ResolveServiceName; intentionally absent from the shared MCPTelemetryConfig",
	"serviceVersion":       "resolved at runtime from binary version (issue #2296)",
	"environmentVariables": "CLI-only, not applicable to CRD-managed telemetry",
	"caCertPath": "filesystem path assigned by runconfig.AddMCPTelemetryConfigRefOptions (cmd/thv-operator/pkg/runconfig/telemetry.go) " +
		"after the operator computes the volume-mount path from openTelemetry.caBundleRef; not user-facing in the CRD",
}

// TestTelemetryConfigDrift exercises the full bidirectional drift contract
// between v1beta1.MCPTelemetryConfigSpec (CRD) and telemetry.Config (runtime)
// via the shared testutil harness. To extend to another domain (OIDC, vMCP,
// etc.), add the three tables in that domain's package and call
// AssertNoDrift with the matching type parameters.
func TestTelemetryConfigDrift(t *testing.T) {
	t.Parallel()
	testutil.AssertNoDrift[telemetry.Config, v1beta1.MCPTelemetryConfigSpec](t, testutil.DriftSpec{
		Domain:               "telemetry",
		Mappings:             telemetryFieldMappings,
		IgnoredOnCRDOnly:     telemetryIgnoredOnCRDOnly,
		IgnoredOnRuntimeOnly: telemetryIgnoredOnRuntimeOnly,
	})
}
