// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package cedar

import (
	"math"
	"testing"

	cedar "github.com/cedar-policy/cedar-go"
	"github.com/cedar-policy/cedar-go/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wantNumberMarker is the expected form of a number marker once its nonce is
// stripped by withoutNonce.
func wantNumberMarker(text string) cedar.Value {
	return cedar.NewRecord(cedar.RecordMap{
		"thv_unrepresentable": cedar.String(unrepresentableNumber),
		"text":                cedar.String(text),
	})
}

// withoutNonce strips the per-conversion nonce from number markers, recursing
// into sets and records, so expected values can be written without it. It
// fails the test if a number marker has no Long nonce.
func withoutNonce(t *testing.T, v cedar.Value) cedar.Value {
	t.Helper()
	switch val := v.(type) {
	case cedar.Record:
		kind, _ := val.Get("thv_unrepresentable")
		isNumber := kind == cedar.String(unrepresentableNumber)
		m := cedar.RecordMap{}
		for k, item := range val.All() {
			if isNumber && k == "nonce" {
				_, ok := item.(cedar.Long)
				require.True(t, ok, "nonce is %T, want Long", item)
				continue
			}
			m[k] = withoutNonce(t, item)
		}
		return cedar.NewRecord(m)
	case cedar.Set:
		var items []cedar.Value
		for item := range val.All() {
			items = append(items, withoutNonce(t, item))
		}
		return cedar.NewSet(items...)
	default:
		return v
	}
}

func mustDecimal(t *testing.T, s string) cedar.Value {
	t.Helper()
	d, err := types.ParseDecimal(s)
	require.NoError(t, err)
	return d
}

// TestConvertMapToCedarRecord tests the convertMapToCedarRecord function.
func TestConvertMapToCedarRecord(t *testing.T) {
	t.Parallel()
	// Test cases
	testCases := []struct {
		name     string
		input    map[string]interface{}
		expected map[string]cedar.Value // Expected key-value pairs in the record
	}{
		{
			name:     "Empty map",
			input:    map[string]interface{}{},
			expected: map[string]cedar.Value{},
		},
		{
			name: "Boolean values",
			input: map[string]interface{}{
				"true_value":  true,
				"false_value": false,
			},
			expected: map[string]cedar.Value{
				"true_value":  cedar.True,
				"false_value": cedar.False,
			},
		},
		{
			name: "String values",
			input: map[string]interface{}{
				"string1": "hello",
				"string2": "world",
			},
			expected: map[string]cedar.Value{
				"string1": cedar.String("hello"),
				"string2": cedar.String("world"),
			},
		},
		{
			name: "Integer values",
			input: map[string]interface{}{
				"int1": 42,
				"int2": int64(9223372036854775807),
			},
			expected: map[string]cedar.Value{
				"int1": cedar.Long(42),
				"int2": cedar.Long(9223372036854775807),
			},
		},
		{
			name: "Float values: exact decimal, or marker when more than four fractional digits",
			input: map[string]interface{}{
				"float1": 3.14,
				"float2": 2.71828,
			},
			expected: map[string]cedar.Value{
				"float1": mustDecimal(t, "3.14"),
				"float2": wantNumberMarker("2.71828"),
			},
		},
		{
			name: "String array values",
			input: map[string]interface{}{
				"roles": []string{"admin", "user", "guest"},
			},
			expected: map[string]cedar.Value{
				"roles": cedar.NewSet(
					cedar.String("admin"),
					cedar.String("user"),
					cedar.String("guest"),
				),
			},
		},
		{
			name: "Interface array values",
			input: map[string]interface{}{
				"mixed": []interface{}{"string", 42, true, 3.14},
			},
			expected: func() map[string]cedar.Value {
				decimal := mustDecimal(t, "3.14")
				return map[string]cedar.Value{
					"mixed": cedar.NewSet(
						cedar.String("string"),
						cedar.Long(42),
						cedar.True,
						decimal,
					),
				}
			}(),
		},
		{
			name: "Mixed types",
			input: map[string]interface{}{
				"string":      "hello",
				"int":         42,
				"bool":        true,
				"float":       3.14,
				"array":       []string{"a", "b", "c"},
				"mixed":       []interface{}{1, "two", true},
				"unsupported": map[string]string{"key": "value"}, // Becomes a marker
			},
			expected: func() map[string]cedar.Value {
				decimal := mustDecimal(t, "3.14")
				return map[string]cedar.Value{
					"string": cedar.String("hello"),
					"int":    cedar.Long(42),
					"bool":   cedar.True,
					"float":  decimal,
					"array": cedar.NewSet(
						cedar.String("a"),
						cedar.String("b"),
						cedar.String("c"),
					),
					"mixed": cedar.NewSet(
						cedar.Long(1),
						cedar.String("two"),
						cedar.True,
					),
					"unsupported": unrepresentableOf(unrepresentableUnsupported, map[string]string{"key": "value"}),
				}
			}(),
		},
		{
			name: "Infinity in array becomes a marker",
			input: map[string]interface{}{
				"mixed": []interface{}{1, "two", true, math.Inf(1)},
			},
			expected: map[string]cedar.Value{
				"mixed": cedar.NewSet(
					cedar.Long(1),
					cedar.String("two"),
					cedar.True,
					wantNumberMarker("+Inf"),
				),
			},
		},
		{
			name: "Infinity value becomes a marker",
			input: map[string]interface{}{
				"invalid_float": math.Inf(1),
			},
			expected: map[string]cedar.Value{
				"invalid_float": wantNumberMarker("+Inf"),
			},
		},
		{
			name: "Null inside an array is dropped",
			input: map[string]interface{}{
				"items": []interface{}{nil, 5.0},
			},
			expected: map[string]cedar.Value{
				"items": cedar.NewSet(mustDecimal(t, "5.0")),
			},
		},
		{
			name: "Null value and null inside an object stay absent",
			input: map[string]interface{}{
				"top":   nil,
				"inner": map[string]interface{}{"gone": nil, "kept": "x"},
			},
			expected: map[string]cedar.Value{
				"inner": cedar.NewRecord(cedar.RecordMap{cedar.String("kept"): cedar.String("x")}),
			},
		},
		{
			name: "Unsupported types become a marker",
			input: map[string]interface{}{
				"struct": struct{ Name string }{"test"},
				"valid":  "this should be included",
			},
			expected: map[string]cedar.Value{
				"valid":  cedar.String("this should be included"),
				"struct": unrepresentableOf(unrepresentableUnsupported, struct{ Name string }{"test"}),
			},
		},
		{
			name: "Nested map converts to Cedar record",
			input: map[string]interface{}{
				"act": map[string]interface{}{"sub": "spiffe://toolhive.dev/ns/agents/sa/devops-agent"},
			},
			expected: map[string]cedar.Value{
				"act": cedar.NewRecord(cedar.RecordMap{
					cedar.String("sub"): cedar.String("spiffe://toolhive.dev/ns/agents/sa/devops-agent"),
				}),
			},
		},
		{
			name: "Deeply nested map",
			input: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": map[string]interface{}{
						"value": "deep",
					},
				},
			},
			expected: map[string]cedar.Value{
				"outer": cedar.NewRecord(cedar.RecordMap{
					cedar.String("inner"): cedar.NewRecord(cedar.RecordMap{
						cedar.String("value"): cedar.String("deep"),
					}),
				}),
			},
		},
		{
			name: "Map nested exactly at depth limit converts fully",
			input: map[string]interface{}{
				"claim": buildNestedMap(maxClaimNestingDepth, "reachable"),
			},
			expected: map[string]cedar.Value{
				"claim": nestedCedarRecord(maxClaimNestingDepth, cedar.String("reachable")),
			},
		},
		{
			name: "Scalar leaf one level past the limit still converts",
			input: map[string]interface{}{
				"claim": buildNestedMap(maxClaimNestingDepth+1, "leaf"),
			},
			expected: map[string]cedar.Value{
				"claim": nestedCedarRecord(maxClaimNestingDepth+1, cedar.String("leaf")),
			},
		},
		{
			name: "Map beyond depth limit ends in a marker",
			input: map[string]interface{}{
				"claim": buildNestedMap(maxClaimNestingDepth+2, "unreachable"),
			},
			expected: map[string]cedar.Value{
				// The container past maxClaimNestingDepth is replaced by a
				// marker instead of being converted.
				"claim": nestedCedarRecord(maxClaimNestingDepth+1,
					unrepresentableOf(unrepresentableTooDeep, buildNestedMap(1, "unreachable"))),
			},
		},
		{
			name: "Scalar leaf one level past the limit in nested arrays still converts",
			input: map[string]interface{}{
				"claim": buildNestedArray(maxClaimNestingDepth+1, "leaf"),
			},
			expected: map[string]cedar.Value{
				"claim": nestedCedarSet(maxClaimNestingDepth+1, cedar.String("leaf")),
			},
		},
		{
			name: "Array beyond depth limit ends in a marker",
			input: map[string]interface{}{
				"claim": buildNestedArray(maxClaimNestingDepth+2, "unreachable"),
			},
			expected: map[string]cedar.Value{
				"claim": nestedCedarSet(maxClaimNestingDepth+1,
					unrepresentableOf(unrepresentableTooDeep, buildNestedArray(1, "unreachable"))),
			},
		},
		{
			name:     "Nil input",
			input:    nil,
			expected: map[string]cedar.Value{},
		},
		{
			name: "Array with false boolean",
			input: map[string]interface{}{
				"bools": []interface{}{false},
			},
			expected: map[string]cedar.Value{
				"bools": cedar.NewSet(cedar.False),
			},
		},
		{
			name: "Array with int64 value",
			input: map[string]interface{}{
				"int64s": []interface{}{int64(9223372036854775807)},
			},
			expected: map[string]cedar.Value{
				"int64s": cedar.NewSet(cedar.Long(9223372036854775807)),
			},
		},
		{
			name: "Array of maps converts to Cedar set of records",
			input: map[string]interface{}{
				"act_chain": []interface{}{
					map[string]interface{}{"sub": "spiffe://toolhive.dev/ns/agents/sa/agent-a"},
					map[string]interface{}{"sub": "spiffe://toolhive.dev/ns/agents/sa/agent-b"},
				},
			},
			expected: map[string]cedar.Value{
				"act_chain": cedar.NewSet(
					cedar.NewRecord(cedar.RecordMap{cedar.String("sub"): cedar.String("spiffe://toolhive.dev/ns/agents/sa/agent-a")}),
					cedar.NewRecord(cedar.RecordMap{cedar.String("sub"): cedar.String("spiffe://toolhive.dev/ns/agents/sa/agent-b")}),
				),
			},
		},
		{
			name: "Array of arrays converts to Cedar set of sets",
			input: map[string]interface{}{
				"groups": []interface{}{
					[]interface{}{"a", "b"},
					[]interface{}{"c"},
				},
			},
			expected: map[string]cedar.Value{
				"groups": cedar.NewSet(
					cedar.NewSet(cedar.String("a"), cedar.String("b")),
					cedar.NewSet(cedar.String("c")),
				),
			},
		},
	}

	// Run test cases
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Create a Cedar record
			record := convertMapToCedarRecord(tc.input)

			// Check that the record has the expected keys and values
			assert.Equal(t, len(tc.expected), record.Len(), "Record has wrong number of entries")

			for k, expectedValue := range tc.expected {
				cedarKey := cedar.String(k)
				actualValue, ok := record.Get(cedarKey)
				assert.True(t, ok, "Key %s not found in record map", k)
				actualValue = withoutNonce(t, actualValue)

				// For decimal values, we need to compare the string representation
				// because Decimal.Equal() is not implemented
				if _, ok := expectedValue.(cedar.Decimal); ok {
					assert.Equal(t, expectedValue.String(), actualValue.String(), "Value for key %s does not match", k)
				} else {
					assert.True(t, expectedValue.Equal(actualValue),
						"Value for key %s does not match: want %s, got %s", k, expectedValue, actualValue)
				}
			}
		})
	}
}

// buildNestedMap wraps leaf in `levels` maps nested under the key "nested",
// e.g. buildNestedMap(2, "x") == map[string]interface{}{"nested":
// map[string]interface{}{"nested": "x"}}. levels must be >= 1 so the return
// value is always a map[string]interface{}. Used to exercise the recursion
// depth cap in convertMapToCedarRecord.
func buildNestedMap(levels int, leaf interface{}) map[string]interface{} {
	value := leaf
	for range levels {
		value = map[string]interface{}{"nested": value}
	}
	return value.(map[string]interface{})
}

// nestedCedarRecord wraps leaf in `levels` Cedar records nested under the
// key "nested" — the expected-value counterpart to buildNestedMap.
func nestedCedarRecord(levels int, leaf cedar.Value) cedar.Value {
	value := leaf
	for range levels {
		value = cedar.NewRecord(cedar.RecordMap{cedar.String("nested"): value})
	}
	return value
}

// TestConvertFloatToCedar pins the exact-or-marker conversion of JSON numbers.
// Inputs are float64 literals on purpose: a Go int would become a Cedar Long
// and skip convertFloatToCedar.
func TestConvertFloatToCedar(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		input      float64
		wantText   string // decimal text, or number text of the marker
		wantMarker bool
	}{
		{name: "zero", input: 0.0, wantText: "0.0"},
		{name: "negative zero", input: math.Copysign(0, -1), wantText: "0.0"},
		{name: "1.1 has no float error", input: 1.1, wantText: "1.1"},
		{name: "0.1", input: 0.1, wantText: "0.1"},
		{name: "0.57 was truncated before", input: 0.57, wantText: "0.57"},
		{name: "19.99 was truncated before", input: 19.99, wantText: "19.99"},
		{name: "four fractional digits", input: 1000.0001, wantText: "1000.0001"},
		{name: "decimal maximum", input: 922337203685477.5, wantText: "922337203685477.5"},
		{name: "five fractional digits", input: 1000.00001, wantText: "1000.00001", wantMarker: true},
		{name: "negative five fractional digits", input: -1000.00001, wantText: "-1000.00001", wantMarker: true},
		{name: "six fractional digits", input: 52.520008, wantText: "52.520008", wantMarker: true},
		{name: "seven fractional digits", input: 1e-7, wantText: "0.0000001", wantMarker: true},
		{name: "just above maximum", input: 922337203685477.58, wantText: "922337203685477.6", wantMarker: true},
		{name: "wrap boundary .6", input: 922337203685477.6, wantText: "922337203685477.6", wantMarker: true},
		{name: "negative wrap boundary", input: -922337203685477.6, wantText: "-922337203685477.6", wantMarker: true},
		{name: "integer above range", input: 922337203685478, wantText: "922337203685478", wantMarker: true},
		{name: "1e15", input: 1e15, wantText: "1000000000000000", wantMarker: true},
		{name: "1e20", input: 1e20, wantText: "100000000000000000000", wantMarker: true},
		{name: "above 2^53 is rounded first", input: 9007199254740993, wantText: "9007199254740992", wantMarker: true},
		{name: "NaN", input: math.NaN(), wantText: "NaN", wantMarker: true},
		{name: "positive infinity", input: math.Inf(1), wantText: "+Inf", wantMarker: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := convertFloatToCedar(tc.input)
			require.NotNil(t, got)
			if !tc.wantMarker {
				d, ok := got.(cedar.Decimal)
				require.True(t, ok, "want decimal, got %T", got)
				assert.Equal(t, mustDecimal(t, tc.wantText).String(), d.String())
				return
			}
			assert.True(t, wantNumberMarker(tc.wantText).Equal(withoutNonce(t, got)))
		})
	}
}

// TestUnrepresentableOfDigest pins that markers compare by content.
func TestUnrepresentableOfDigest(t *testing.T) {
	t.Parallel()

	a := unrepresentableOf(unrepresentableTooDeep, []interface{}{"a"})
	assert.True(t, a.Equal(unrepresentableOf(unrepresentableTooDeep, []interface{}{"a"})))
	assert.False(t, a.Equal(unrepresentableOf(unrepresentableTooDeep, []interface{}{"b"})))
	assert.False(t, a.Equal(unrepresentableOf(unrepresentableUnsupported, []interface{}{"a"})))

	// A value JSON cannot encode still gets a digest that differs per value.
	nan := unrepresentableOf(unrepresentableTooDeep, []interface{}{math.NaN()})
	inf := unrepresentableOf(unrepresentableTooDeep, []interface{}{math.Inf(1)})
	assert.False(t, nan.Equal(inf))
}

// TestNumberMarkersNeverEqual pins that two markers for the same number differ.
func TestNumberMarkersNeverEqual(t *testing.T) {
	t.Parallel()

	assert.False(t, convertFloatToCedar(1e15).Equal(convertFloatToCedar(1e15)))
}

// buildNestedArray wraps leaf in `levels` single-element arrays.
func buildNestedArray(levels int, leaf interface{}) []interface{} {
	value := leaf
	for range levels {
		value = []interface{}{value}
	}
	return value.([]interface{})
}

// nestedCedarSet wraps leaf in `levels` single-element Cedar sets, the
// expected-value counterpart to buildNestedArray.
func nestedCedarSet(levels int, leaf cedar.Value) cedar.Value {
	value := leaf
	for range levels {
		value = cedar.NewSet(value)
	}
	return value
}
