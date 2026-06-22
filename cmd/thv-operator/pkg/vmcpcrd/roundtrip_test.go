// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcpcrd_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/randfill"

	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/vmcpcrd"
	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

// TestRoundTripTranscode fuzzes a vmcpcrd.Config, transcodes it to
// config.Config exactly the way the converter's crdToRuntime does (json.Marshal
// then json.Unmarshal), transcodes it back, and asserts the value survives the
// double crossing. This catches value loss in the JSON boundary that schema
// parity alone cannot: a field present on both sides but serialized under
// different semantics would silently drop here.
//
// Seeds are the loop index, not wall-clock time — the run is fully
// deterministic and reproducible.
func TestRoundTripTranscode(t *testing.T) {
	t.Parallel()

	const iterations = 100
	for i := 0; i < iterations; i++ {
		i := i
		t.Run(fmt.Sprintf("seed-%d", i), func(t *testing.T) {
			t.Parallel()

			original := fuzzMirrorConfig(int64(i))

			// Forward transcode: vmcpcrd.Config -> config.Config, identical to
			// crdToRuntime in cmd/thv-operator/pkg/vmcpconfig/converter.go.
			runtime := transcode[config.Config](t, original)

			// Reverse transcode back into the mirror type.
			roundTripped := transcode[vmcpcrd.Config](t, runtime)

			// thvjson.Map / thvjson.Any decode JSON numbers as float64 and store
			// arbitrary interface{} content, so reflect.DeepEqual on the structs
			// can report spurious mismatches even when the JSON is identical
			// (e.g. an int that became a float64). We populate those fields with
			// only string-valued, JSON-stable content in the fuzzer, so
			// DeepEqual is reliable here; but to be robust against any residual
			// interface{} ambiguity we compare canonical JSON as the source of
			// truth and use DeepEqual as a secondary, stricter check.
			origJSON := mustMarshal(t, original)
			rtJSON := mustMarshal(t, roundTripped)
			require.JSONEq(t, string(origJSON), string(rtJSON),
				"canonical JSON diverged after round-trip transcode (seed %d)", i)

			require.True(t, reflect.DeepEqual(original, roundTripped),
				"vmcpcrd.Config did not survive round-trip transcode (seed %d)\noriginal:    %s\nroundtripped: %s",
				i, origJSON, rtJSON)
		})
	}
}

// transcode mirrors crdToRuntime: marshal the source to JSON, then unmarshal
// into T. Any error is a genuine schema/converter defect, so the test fails.
func transcode[T any](t *testing.T, src any) T {
	t.Helper()
	data, err := json.Marshal(src)
	require.NoError(t, err, "marshal source for transcode")
	var out T
	require.NoError(t, json.Unmarshal(data, &out), "unmarshal into %T", out)
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

// fuzzMirrorConfig builds a deterministically-seeded, fully-populated
// vmcpcrd.Config. Custom fill funcs handle the three custom-marshaler field
// families that a naive fuzz would corrupt:
//   - vmcpcrd.Duration marshals as a Go duration string, so it must be filled
//     with a whole-unit duration that round-trips cleanly ("42s", not "42ns"
//     which is fine too, but fractional/odd values stay exact in seconds).
//   - thvjson.Map / thvjson.Any wrap interface{} content; we fill them with a
//     small map of string->string so the value survives JSON (no int/float64
//     ambiguity, no NaN, no key reordering issues).
func fuzzMirrorConfig(seed int64) vmcpcrd.Config {
	f := randfill.NewWithSeed(seed).
		// Always populate optional pointers/maps/slices so the round-trip
		// actually exercises every field rather than leaving them nil.
		NilChance(0).
		NumElements(1, 3).
		MaxDepth(8).
		Funcs(
			// Duration: whole seconds keep the string form lossless ("Ns").
			func(d *vmcpcrd.Duration, c randfill.Continue) {
				secs := c.Int63n(3600) // 0..1h, whole seconds
				*d = vmcpcrd.Duration(time.Duration(secs) * time.Second)
			},
			// thvjson.Map: JSON object with only string values.
			func(m *thvjson.Map, c randfill.Continue) {
				*m = thvjson.NewMap(stableStringMap(c))
			},
			// thvjson.Any: store a JSON-stable string map as well.
			func(a *thvjson.Any, c randfill.Continue) {
				*a = thvjson.NewAny(stableStringMap(c))
			},
		)

	var cfg vmcpcrd.Config
	f.Fill(&cfg)
	return cfg
}

// stableStringMap produces a small map[string]any whose values are all strings,
// guaranteeing it survives a JSON round-trip with reflect.DeepEqual intact.
func stableStringMap(c randfill.Continue) map[string]any {
	n := 1 + c.Intn(3)
	m := make(map[string]any, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("k%d_%s", i, c.String(4))] = c.String(6)
	}
	return m
}
