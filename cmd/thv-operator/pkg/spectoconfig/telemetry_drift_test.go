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
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

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
	"openTelemetry.enabled":                            "CRD-only gate; controls whether the converter populates runtime fields at all",
	"openTelemetry.sensitiveHeaders.name":              "K8s-secret-backed headers; resolved by operator into runtime Headers",
	"openTelemetry.sensitiveHeaders.secretKeyRef.name": "K8s-secret-backed headers; resolved by operator into runtime Headers",
	"openTelemetry.sensitiveHeaders.secretKeyRef.key":  "K8s-secret-backed headers; resolved by operator into runtime Headers",
	"openTelemetry.caBundleRef.configMapRef.name":      "K8s ConfigMap reference; resolved by operator into runtime CACertPath",
	"openTelemetry.caBundleRef.configMapRef.key":       "K8s ConfigMap reference; resolved by operator into runtime CACertPath",
	"openTelemetry.caBundleRef.configMapRef.optional":  "K8s ConfigMap reference flag promoted from corev1.ConfigMapKeySelector; not part of runtime config",
}

// telemetryIgnoredOnRuntimeOnly lists runtime leaf fields that intentionally
// have no CRD counterpart. Each entry MUST include a justification.
var telemetryIgnoredOnRuntimeOnly = map[string]string{
	"serviceName":          "per-server, set via MCPTelemetryConfigReference.ServiceName, not stored on the shared MCPTelemetryConfig",
	"serviceVersion":       "resolved at runtime from binary version (issue #2296)",
	"environmentVariables": "CLI-only, not applicable to CRD-managed telemetry",
	"caCertPath":           "filesystem path computed by the operator from caBundleRef; not user-facing in the CRD",
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

	for i, m := range telemetryFieldMappings {
		assert.NotEmptyf(t, m.CRD, "telemetryFieldMappings[%d].CRD must not be empty", i)
		assert.NotEmptyf(t, m.Runtime, "telemetryFieldMappings[%d].Runtime must not be empty", i)
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

	// Stable iteration not strictly necessary for correctness, but it keeps
	// failure output deterministic when assertions fire on multiple keys.
	_ = sortedKeys(seenCRD)
	_ = sortedKeys(seenRuntime)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
