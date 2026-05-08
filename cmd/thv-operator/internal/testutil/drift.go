// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// FieldMapping pairs a CRD-side leaf path with a runtime-side leaf path.
// Both sides must be present unless the field is in one of the ignore maps
// of the enclosing DriftSpec.
type FieldMapping struct {
	CRD     string
	Runtime string
}

// DriftSpec declares the bidirectional mapping between a CRD spec type and
// its runtime config counterpart. Every leaf path on either type must be
// classified into exactly one of three buckets: a Mappings entry, an
// IgnoredOnCRDOnly entry (with justification), or an IgnoredOnRuntimeOnly
// entry (with justification). AssertNoDrift fails the test when a leaf is
// unclassified, when a table entry is stale, or when the tables contradict
// themselves.
//
// Domain is used in failure messages and subtest names; pick something that
// matches the domain folder (e.g. "telemetry", "oidc", "vmcp").
type DriftSpec struct {
	Domain               string
	Mappings             []FieldMapping
	IgnoredOnCRDOnly     map[string]string
	IgnoredOnRuntimeOnly map[string]string
}

// AssertNoDrift runs three subtests against spec:
//   - CRDFieldsCovered:    every CRD leaf is mapped or ignored
//   - RuntimeFieldsCovered: every runtime leaf is mapped or ignored
//   - MappingTableSanity:  tables have no duplicates, empty entries,
//     cross-pollination, or stale leaves
//
// Type parameter ordering is [Runtime, CRD] mirroring how a converter reads:
// it produces Runtime from CRD. Failure messages reference both type names so
// a developer who hits a failure can grep directly to the offending field.
//
//	testutil.AssertNoDrift[telemetry.Config, v1beta1.MCPTelemetryConfigSpec](
//	    t,
//	    testutil.DriftSpec{
//	        Domain:               "telemetry",
//	        Mappings:             telemetryFieldMappings,
//	        IgnoredOnCRDOnly:     telemetryIgnoredOnCRDOnly,
//	        IgnoredOnRuntimeOnly: telemetryIgnoredOnRuntimeOnly,
//	    },
//	)
func AssertNoDrift[Runtime, CRD any](t *testing.T, spec DriftSpec) {
	t.Helper()

	require.NotEmptyf(t, spec.Domain, "DriftSpec.Domain is required")

	var (
		crdType     = reflect.TypeOf((*CRD)(nil)).Elem()
		runtimeType = reflect.TypeOf((*Runtime)(nil)).Elem()
	)

	t.Run("CRDFieldsCovered", func(t *testing.T) {
		t.Parallel()
		assertSideCovered(t, crdType, sideCRD, spec)
	})
	t.Run("RuntimeFieldsCovered", func(t *testing.T) {
		t.Parallel()
		assertSideCovered(t, runtimeType, sideRuntime, spec)
	})
	t.Run("MappingTableSanity", func(t *testing.T) {
		t.Parallel()
		assertTableSanity(t, crdType, runtimeType, spec)
	})
}

// side identifies which half of the boundary a leaf belongs to. Internal to
// the helper — callers don't see it.
type side int

const (
	sideCRD side = iota
	sideRuntime
)

func (s side) typeLabel() string {
	if s == sideCRD {
		return "CRD"
	}
	return "Runtime"
}

func assertSideCovered(t *testing.T, typ reflect.Type, s side, spec DriftSpec) {
	t.Helper()

	mapped := make(map[string]struct{}, len(spec.Mappings))
	for _, m := range spec.Mappings {
		switch s {
		case sideCRD:
			mapped[m.CRD] = struct{}{}
		case sideRuntime:
			mapped[m.Runtime] = struct{}{}
		}
	}

	ignored := spec.IgnoredOnCRDOnly
	siblingTable := "IgnoredOnCRDOnly"
	pairedSidePath := "Runtime"
	if s == sideRuntime {
		ignored = spec.IgnoredOnRuntimeOnly
		siblingTable = "IgnoredOnRuntimeOnly"
		pairedSidePath = "CRD"
	}

	leaves := FlattenJSONLeafFields(typ)
	for _, leaf := range leaves {
		if _, ok := mapped[leaf]; ok {
			continue
		}
		if _, ok := ignored[leaf]; ok {
			continue
		}
		t.Errorf(
			"%s drift: %s field %q on type %s is unclassified.\n"+
				"Action: add it to Mappings (paired with the corresponding %s path)\n"+
				"        OR add it to %s with a justification string.",
			spec.Domain, s.typeLabel(), leaf, typ.String(), pairedSidePath, siblingTable,
		)
	}
}

func assertTableSanity(t *testing.T, crdType, runtimeType reflect.Type, spec DriftSpec) {
	t.Helper()
	assertNoEmptyOrDuplicateMappings(t, spec)
	assertNoMappedAndIgnored(t, spec)
	assertJustificationsNonEmpty(t, spec)
	assertNoCrossPollination(t, spec)
	assertNoStaleEntries(t, sideCRD, crdType, spec)
	assertNoStaleEntries(t, sideRuntime, runtimeType, spec)
}

// assertNoEmptyOrDuplicateMappings requires both halves of every Mappings
// entry to be non-empty (using require so an empty key doesn't pollute the
// duplicate-detection map with cascading failures) and asserts that no path
// on either side appears more than once.
func assertNoEmptyOrDuplicateMappings(t *testing.T, spec DriftSpec) {
	t.Helper()
	seenCRD := make(map[string]int, len(spec.Mappings))
	seenRuntime := make(map[string]int, len(spec.Mappings))
	for i, m := range spec.Mappings {
		require.NotEmptyf(t, m.CRD, "%s: Mappings[%d].CRD must not be empty", spec.Domain, i)
		require.NotEmptyf(t, m.Runtime, "%s: Mappings[%d].Runtime must not be empty", spec.Domain, i)
		seenCRD[m.CRD]++
		seenRuntime[m.Runtime]++
	}
	for path, count := range seenCRD {
		assert.Equalf(t, 1, count, "%s: CRD path %q appears %d times in Mappings", spec.Domain, path, count)
	}
	for path, count := range seenRuntime {
		assert.Equalf(t, 1, count, "%s: runtime path %q appears %d times in Mappings", spec.Domain, path, count)
	}
}

// assertNoMappedAndIgnored fails if a Mappings entry's CRD or Runtime path
// also appears in the corresponding ignore map. A path can be classified as
// mapped or as ignored, never both.
func assertNoMappedAndIgnored(t *testing.T, spec DriftSpec) {
	t.Helper()
	for _, m := range spec.Mappings {
		if _, dup := spec.IgnoredOnCRDOnly[m.CRD]; dup {
			t.Errorf("%s: CRD path %q is both mapped and listed in IgnoredOnCRDOnly", spec.Domain, m.CRD)
		}
		if _, dup := spec.IgnoredOnRuntimeOnly[m.Runtime]; dup {
			t.Errorf("%s: runtime path %q is both mapped and listed in IgnoredOnRuntimeOnly", spec.Domain, m.Runtime)
		}
	}
}

// assertJustificationsNonEmpty checks every entry in both ignore maps has
// a non-empty justification string.
func assertJustificationsNonEmpty(t *testing.T, spec DriftSpec) {
	t.Helper()
	for path, reason := range spec.IgnoredOnCRDOnly {
		assert.NotEmptyf(t, reason, "%s: IgnoredOnCRDOnly[%q] must include a justification", spec.Domain, path)
	}
	for path, reason := range spec.IgnoredOnRuntimeOnly {
		assert.NotEmptyf(t, reason, "%s: IgnoredOnRuntimeOnly[%q] must include a justification", spec.Domain, path)
	}
}

// assertNoCrossPollination fails if a path is classified as ignored on both
// sides — usually a copy-paste mistake when shifting a field across the
// boundary.
func assertNoCrossPollination(t *testing.T, spec DriftSpec) {
	t.Helper()
	for path := range spec.IgnoredOnCRDOnly {
		if _, dup := spec.IgnoredOnRuntimeOnly[path]; dup {
			t.Errorf("%s: path %q is listed in BOTH IgnoredOnCRDOnly and IgnoredOnRuntimeOnly", spec.Domain, path)
		}
	}
}

// assertNoStaleEntries fails for any mapping/ignore-table entry that is no
// longer a live leaf on the given type. Catches entries left behind after a
// field rename or deletion, which would otherwise mask the change.
func assertNoStaleEntries(t *testing.T, s side, typ reflect.Type, spec DriftSpec) {
	t.Helper()
	leaves := liveLeafSet(typ)

	mappingPath := func(m FieldMapping) string {
		if s == sideCRD {
			return m.CRD
		}
		return m.Runtime
	}
	ignoreTable := spec.IgnoredOnCRDOnly
	ignoreLabel := "IgnoredOnCRDOnly"
	if s == sideRuntime {
		ignoreTable = spec.IgnoredOnRuntimeOnly
		ignoreLabel = "IgnoredOnRuntimeOnly"
	}

	for _, m := range spec.Mappings {
		path := mappingPath(m)
		if _, live := leaves[path]; !live {
			t.Errorf("%s: Mappings entry %q is not a live leaf on %s — stale entry?", spec.Domain, path, typ.String())
		}
	}
	for path := range ignoreTable {
		if _, live := leaves[path]; !live {
			t.Errorf("%s: %s entry %q is not a live leaf on %s — stale entry?", spec.Domain, ignoreLabel, path, typ.String())
		}
	}
}

func liveLeafSet(t reflect.Type) map[string]struct{} {
	leaves := FlattenJSONLeafFields(t)
	out := make(map[string]struct{}, len(leaves))
	for _, l := range leaves {
		out[l] = struct{}{}
	}
	return out
}
