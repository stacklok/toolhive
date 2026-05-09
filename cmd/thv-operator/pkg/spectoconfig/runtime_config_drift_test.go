// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spectoconfig

import (
	"reflect"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/cmd/thv-operator/internal/testutil"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// runtimeOnlyLeafJustifications enumerates leaf paths that exist on
// RuntimeConfig but NOT on Config. Each entry must include a justification
// explaining why the field is operator-resolved and therefore must not
// appear in the public CRD schema.
//
// Today RuntimeConfig adds nothing extra, so this map is empty. The drift
// test below fails if anyone adds a field to RuntimeConfig without
// allowlisting it here, which is the forcing function that turns "I'll
// just stick this on RuntimeConfig" into a deliberate review conversation.
//
// When future operator-resolved fields land (e.g. a backendHeaderForward
// map populated from MCPServerEntry.spec.headerForward), add the leaf path
// here with a justification that names the operator code that populates it.
var runtimeOnlyLeafJustifications = map[string]string{}

// TestRuntimeConfigSeam guards the wrapper relationship between Config (the
// public CRD-visible type) and RuntimeConfig (the operator-write surface):
//
//   - RuntimeConfig MUST contain every Config leaf. RuntimeConfig embeds
//     Config inline, so today this is automatic — but the test pins it so
//     a future refactor that, say, replaces the embed with a reference
//     would fail loudly instead of silently dropping fields from the
//     ConfigMap.
//
//   - Every leaf on RuntimeConfig that is NOT on Config must appear in
//     runtimeOnlyLeafJustifications with an explanation. These are
//     operator-resolved sidecars (secret-identifier maps, resolved file
//     paths, etc.) that travel through the ConfigMap to the vMCP runtime
//     without becoming public API surface.
//
//   - Conversely, no Config leaf may be allowlisted in
//     runtimeOnlyLeafJustifications — that would be a contradiction.
//
// The test catches the mistake the wrapper exists to prevent: adding an
// operator-resolved field directly onto Config (where it leaks into the
// CRD) instead of onto RuntimeConfig.
func TestRuntimeConfigSeam(t *testing.T) {
	t.Parallel()

	configLeaves := setOf(testutil.FlattenJSONLeafFields(reflect.TypeOf(vmcpconfig.Config{})))
	runtimeLeaves := setOf(testutil.FlattenJSONLeafFields(reflect.TypeOf(vmcpconfig.RuntimeConfig{})))

	t.Run("RuntimeConfig is a superset of Config", func(t *testing.T) {
		t.Parallel()
		var missing []string
		for leaf := range configLeaves {
			if _, ok := runtimeLeaves[leaf]; !ok {
				missing = append(missing, leaf)
			}
		}
		sort.Strings(missing)
		assert.Empty(
			t, missing,
			"every Config leaf must also be a leaf on RuntimeConfig (RuntimeConfig embeds Config inline). "+
				"Missing: %v", missing,
		)
	})

	t.Run("RuntimeConfig extras are justified", func(t *testing.T) {
		t.Parallel()
		for leaf := range runtimeLeaves {
			if _, onConfig := configLeaves[leaf]; onConfig {
				continue
			}
			reason, ok := runtimeOnlyLeafJustifications[leaf]
			if !ok {
				t.Errorf(
					"RuntimeConfig leaf %q is not present on Config and is not allowlisted "+
						"in runtimeOnlyLeafJustifications.\n"+
						"Action: if this field is operator-resolved (must NOT appear in the CRD), "+
						"add it to runtimeOnlyLeafJustifications with a justification. "+
						"Otherwise, move it onto Config so the CRD picks it up.",
					leaf,
				)
				continue
			}
			require.NotEmptyf(t, reason, "runtimeOnlyLeafJustifications[%q] must include a justification", leaf)
		}
	})

	t.Run("allowlist has no stale or self-contradicting entries", func(t *testing.T) {
		t.Parallel()
		for leaf := range runtimeOnlyLeafJustifications {
			if _, onConfig := configLeaves[leaf]; onConfig {
				t.Errorf(
					"%q is allowlisted as runtime-only but also exists on Config — contradicting classification",
					leaf,
				)
			}
			if _, onRuntime := runtimeLeaves[leaf]; !onRuntime {
				t.Errorf(
					"%q is allowlisted as runtime-only but is not a live leaf on RuntimeConfig — stale entry?",
					leaf,
				)
			}
		}
	})
}

func setOf(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}
