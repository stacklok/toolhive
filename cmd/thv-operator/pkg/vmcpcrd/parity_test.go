// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package vmcpcrd_test holds drift-detection tests that must import both the
// operator-owned CRD mirror (cmd/thv-operator/pkg/vmcpcrd) and the internal
// runtime config model (pkg/vmcp/config). It lives in an external test package
// so the non-test vmcpcrd package never takes a build-time dependency on
// pkg/vmcp/config — the whole point of the decoupling.
package vmcpcrd_test

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpcrd"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestConfigSchemaParity asserts that the operator-owned vmcpcrd.Config mirror
// has the exact same set of JSON leaf paths as the runtime config.Config. This
// is the field-for-field parity guarantee: when one side gains, renames, or
// drops a field, the symmetric difference below names the drifted leaf so the
// developer immediately knows which side to fix.
func TestConfigSchemaParity(t *testing.T) {
	t.Parallel()

	runtimeLeaves := testutil.FlattenJSONLeafFields(reflect.TypeOf(config.Config{}))
	mirrorLeaves := testutil.FlattenJSONLeafFields(reflect.TypeOf(vmcpcrd.Config{}))

	if reflect.DeepEqual(runtimeLeaves, mirrorLeaves) {
		return
	}

	onlyInRuntime := setDifference(runtimeLeaves, mirrorLeaves)
	onlyInMirror := setDifference(mirrorLeaves, runtimeLeaves)

	t.Errorf(
		"vmcpcrd.Config and config.Config JSON schemas have drifted.\n"+
			"Leaves only in config.Config (pkg/vmcp/config): %s\n"+
			"Leaves only in vmcpcrd.Config  (operator mirror): %s\n"+
			"Action: re-sync the two struct definitions field-for-field (and the converter) until they match.",
		formatLeaves(onlyInRuntime), formatLeaves(onlyInMirror),
	)
}

// setDifference returns the elements present in a but not in b, sorted.
func setDifference(a, b []string) []string {
	bSet := make(map[string]struct{}, len(b))
	for _, s := range b {
		bSet[s] = struct{}{}
	}
	var out []string
	for _, s := range a {
		if _, ok := bSet[s]; !ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// formatLeaves renders a leaf list for a failure message, using "(none)" when
// empty so the message is unambiguous.
func formatLeaves(leaves []string) string {
	if len(leaves) == 0 {
		return "(none)"
	}
	return "[" + strings.Join(leaves, ", ") + "]"
}
