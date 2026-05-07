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
// be updated. The "Mapping table sanity" test guards the tables themselves
// (no duplicates, no empty entries, no overlap with the ignore lists).

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/pkg/telemetry"
)

// FieldMapping pairs a CRD-side leaf path with a runtime-side leaf path. Both
// sides must be present unless the field is in one of the ignore maps.
type FieldMapping struct {
	CRD     string
	Runtime string
}

// telemetryFieldMappings is the source of truth for CRD<->runtime field
// links. One entry per leaf field that exists on both sides. The CRD path is
// rooted at MCPTelemetryConfigSpec; the runtime path is rooted at
// telemetry.Config.
var telemetryFieldMappings = []FieldMapping{
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
	"caCertPath": "filesystem path injected by runconfig.AppendTelemetryRunnerOption after the operator computes the volume mount " +
		"path from openTelemetry.caBundleRef; not user-facing in the CRD",
}

// TestTelemetryConfigDrift_CRDFieldsCovered walks MCPTelemetryConfigSpec and
// requires every leaf path to appear either as a CRD entry in
// telemetryFieldMappings or as a key in telemetryIgnoredOnCRDOnly.
func TestTelemetryConfigDrift_CRDFieldsCovered(t *testing.T) {
	t.Parallel()

	mappedCRD := make(map[string]struct{}, len(telemetryFieldMappings))
	for _, m := range telemetryFieldMappings {
		mappedCRD[m.CRD] = struct{}{}
	}

	leaves := testutil.FlattenJSONLeafFields(reflect.TypeOf(v1beta1.MCPTelemetryConfigSpec{}))
	for _, leaf := range leaves {
		if _, ok := mappedCRD[leaf]; ok {
			continue
		}
		if _, ok := telemetryIgnoredOnCRDOnly[leaf]; ok {
			continue
		}
		t.Errorf(
			"v1beta1.MCPTelemetryConfigSpec field %q is unclassified.\n"+
				"Action: add it to telemetryFieldMappings (with the corresponding telemetry.Config path)\n"+
				"        OR add it to telemetryIgnoredOnCRDOnly with a justification string.",
			leaf,
		)
	}
}

// TestTelemetryConfigDrift_RuntimeFieldsCovered walks telemetry.Config and
// requires every leaf path to appear either as a Runtime entry in
// telemetryFieldMappings or as a key in telemetryIgnoredOnRuntimeOnly.
func TestTelemetryConfigDrift_RuntimeFieldsCovered(t *testing.T) {
	t.Parallel()

	mappedRuntime := make(map[string]struct{}, len(telemetryFieldMappings))
	for _, m := range telemetryFieldMappings {
		mappedRuntime[m.Runtime] = struct{}{}
	}

	leaves := testutil.FlattenJSONLeafFields(reflect.TypeOf(telemetry.Config{}))
	for _, leaf := range leaves {
		if _, ok := mappedRuntime[leaf]; ok {
			continue
		}
		if _, ok := telemetryIgnoredOnRuntimeOnly[leaf]; ok {
			continue
		}
		t.Errorf(
			"telemetry.Config field %q is unclassified.\n"+
				"Action: add it to telemetryFieldMappings (with the corresponding MCPTelemetryConfigSpec path)\n"+
				"        OR add it to telemetryIgnoredOnRuntimeOnly with a justification string.",
			leaf,
		)
	}
}

// TestTelemetryConfigDrift_MappingTableSanity guards the mapping tables
// themselves. It catches mistakes like duplicate paths, empty entries, and
// overlap between mapped and ignored fields.
func TestTelemetryConfigDrift_MappingTableSanity(t *testing.T) {
	t.Parallel()

	seenCRD := make(map[string]int, len(telemetryFieldMappings))
	seenRuntime := make(map[string]int, len(telemetryFieldMappings))

	// Use require for the per-entry NotEmpty checks so that an empty CRD
	// or Runtime field doesn't pollute the duplicate maps below with an
	// empty-string key — that would trigger a misleading cascade.
	for i, m := range telemetryFieldMappings {
		require.NotEmptyf(t, m.CRD, "telemetryFieldMappings[%d].CRD must not be empty", i)
		require.NotEmptyf(t, m.Runtime, "telemetryFieldMappings[%d].Runtime must not be empty", i)
		seenCRD[m.CRD]++
		seenRuntime[m.Runtime]++
	}

	for path, count := range seenCRD {
		assert.Equalf(t, 1, count, "CRD path %q appears %d times in telemetryFieldMappings", path, count)
	}
	for path, count := range seenRuntime {
		assert.Equalf(t, 1, count, "runtime path %q appears %d times in telemetryFieldMappings", path, count)
	}

	// Overlap with ignore lists.
	for _, m := range telemetryFieldMappings {
		if _, dup := telemetryIgnoredOnCRDOnly[m.CRD]; dup {
			t.Errorf("CRD path %q is both mapped and listed in telemetryIgnoredOnCRDOnly", m.CRD)
		}
		if _, dup := telemetryIgnoredOnRuntimeOnly[m.Runtime]; dup {
			t.Errorf("runtime path %q is both mapped and listed in telemetryIgnoredOnRuntimeOnly", m.Runtime)
		}
	}

	// Justifications must be non-empty.
	for path, reason := range telemetryIgnoredOnCRDOnly {
		assert.NotEmptyf(t, reason, "telemetryIgnoredOnCRDOnly[%q] must include a justification", path)
	}
	for path, reason := range telemetryIgnoredOnRuntimeOnly {
		assert.NotEmptyf(t, reason, "telemetryIgnoredOnRuntimeOnly[%q] must include a justification", path)
	}

	// A leaf path can't be classified two different ways. An entry in both
	// ignore maps is a copy-paste mistake when shifting a field across the
	// boundary — fail loudly instead of silently allowing the contradiction.
	for path := range telemetryIgnoredOnCRDOnly {
		if _, dup := telemetryIgnoredOnRuntimeOnly[path]; dup {
			t.Errorf("path %q is listed in BOTH telemetryIgnoredOnCRDOnly and telemetryIgnoredOnRuntimeOnly", path)
		}
	}

	// Every path in the mapping/ignore tables must still be a live leaf on
	// its respective type. Catches stale entries left behind by field
	// renames or deletions, which would otherwise mask the rename.
	crdLeaves := liveLeafSet(reflect.TypeOf(v1beta1.MCPTelemetryConfigSpec{}))
	for _, m := range telemetryFieldMappings {
		if _, live := crdLeaves[m.CRD]; !live {
			t.Errorf("telemetryFieldMappings entry %q is not a live leaf on v1beta1.MCPTelemetryConfigSpec — stale entry?", m.CRD)
		}
	}
	for path := range telemetryIgnoredOnCRDOnly {
		if _, live := crdLeaves[path]; !live {
			t.Errorf("telemetryIgnoredOnCRDOnly entry %q is not a live leaf on v1beta1.MCPTelemetryConfigSpec — stale entry?", path)
		}
	}
	runtimeLeaves := liveLeafSet(reflect.TypeOf(telemetry.Config{}))
	for _, m := range telemetryFieldMappings {
		if _, live := runtimeLeaves[m.Runtime]; !live {
			t.Errorf("telemetryFieldMappings entry %q is not a live leaf on telemetry.Config — stale entry?", m.Runtime)
		}
	}
	for path := range telemetryIgnoredOnRuntimeOnly {
		if _, live := runtimeLeaves[path]; !live {
			t.Errorf("telemetryIgnoredOnRuntimeOnly entry %q is not a live leaf on telemetry.Config — stale entry?", path)
		}
	}
}

// liveLeafSet returns the set of leaf paths reachable from t, for use in
// stale-entry checks against the drift mapping/ignore tables.
func liveLeafSet(t reflect.Type) map[string]struct{} {
	leaves := testutil.FlattenJSONLeafFields(t)
	out := make(map[string]struct{}, len(leaves))
	for _, l := range leaves {
		out[l] = struct{}{}
	}
	return out
}
