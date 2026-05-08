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

	seenCRD := make(map[string]int, len(spec.Mappings))
	seenRuntime := make(map[string]int, len(spec.Mappings))

	// require so that an empty CRD/Runtime field doesn't pollute the dup
	// maps below with empty-string keys (which would cause cascading
	// misleading failures).
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

	// Overlap between mapped and ignored on the same side.
	for _, m := range spec.Mappings {
		if _, dup := spec.IgnoredOnCRDOnly[m.CRD]; dup {
			t.Errorf("%s: CRD path %q is both mapped and listed in IgnoredOnCRDOnly", spec.Domain, m.CRD)
		}
		if _, dup := spec.IgnoredOnRuntimeOnly[m.Runtime]; dup {
			t.Errorf("%s: runtime path %q is both mapped and listed in IgnoredOnRuntimeOnly", spec.Domain, m.Runtime)
		}
	}

	// Justifications must be non-empty.
	for path, reason := range spec.IgnoredOnCRDOnly {
		assert.NotEmptyf(t, reason, "%s: IgnoredOnCRDOnly[%q] must include a justification", spec.Domain, path)
	}
	for path, reason := range spec.IgnoredOnRuntimeOnly {
		assert.NotEmptyf(t, reason, "%s: IgnoredOnRuntimeOnly[%q] must include a justification", spec.Domain, path)
	}

	// Cross-pollination: a path can't be classified two different ways.
	for path := range spec.IgnoredOnCRDOnly {
		if _, dup := spec.IgnoredOnRuntimeOnly[path]; dup {
			t.Errorf("%s: path %q is listed in BOTH IgnoredOnCRDOnly and IgnoredOnRuntimeOnly", spec.Domain, path)
		}
	}

	// Stale-leaf check: every path in mapping/ignore tables must still be a
	// live leaf on its respective type. Catches table entries left behind
	// after a field rename or deletion, which would otherwise mask the
	// change.
	crdLeaves := liveLeafSet(crdType)
	for _, m := range spec.Mappings {
		if _, live := crdLeaves[m.CRD]; !live {
			t.Errorf("%s: Mappings entry %q is not a live leaf on %s — stale entry?", spec.Domain, m.CRD, crdType.String())
		}
	}
	for path := range spec.IgnoredOnCRDOnly {
		if _, live := crdLeaves[path]; !live {
			t.Errorf("%s: IgnoredOnCRDOnly entry %q is not a live leaf on %s — stale entry?", spec.Domain, path, crdType.String())
		}
	}
	runtimeLeaves := liveLeafSet(runtimeType)
	for _, m := range spec.Mappings {
		if _, live := runtimeLeaves[m.Runtime]; !live {
			t.Errorf("%s: Mappings entry %q is not a live leaf on %s — stale entry?", spec.Domain, m.Runtime, runtimeType.String())
		}
	}
	for path := range spec.IgnoredOnRuntimeOnly {
		if _, live := runtimeLeaves[path]; !live {
			t.Errorf("%s: IgnoredOnRuntimeOnly entry %q is not a live leaf on %s — stale entry?", spec.Domain, path, runtimeType.String())
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
