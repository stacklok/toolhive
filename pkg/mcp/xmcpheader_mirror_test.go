// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParamHeadersForSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema map[string]any
		args   map[string]any
		want   map[string]string
	}{
		{
			name:   "no annotations mirrors nothing",
			schema: objSchema(map[string]any{"query": map[string]any{"type": "string"}}),
			args:   map[string]any{"query": "select 1"},
		},
		{
			// The SEP's worked example: region is designated, query is not.
			name: "designated string is mirrored and others are not",
			schema: objSchema(map[string]any{
				"region": annotated("string", "Region"),
				"query":  map[string]any{"type": "string"},
			}),
			args: map[string]any{"region": "eu-west1", "query": "select 1"},
			want: map[string]string{"Mcp-Param-Region": "eu-west1"},
		},
		{
			name: "designated empty string is sentinel-encoded",
			schema: objSchema(map[string]any{
				"region": annotated("string", "Region"),
			}),
			args: map[string]any{"region": ""},
			want: map[string]string{"Mcp-Param-Region": "=?base64??="},
		},
		{
			name:   "boolean is mirrored as true/false",
			schema: objSchema(map[string]any{"dry_run": annotated("boolean", "Dry-Run")}),
			args:   map[string]any{"dry_run": true},
			want:   map[string]string{"Mcp-Param-Dry-Run": "true"},
		},
		{
			// JSON decoding yields float64 for every number, so the integral
			// float path is the one real clients exercise.
			name:   "integer arriving as a JSON float is mirrored without a decimal point",
			schema: objSchema(map[string]any{"attempts": annotated("integer", "Attempts")}),
			args:   map[string]any{"attempts": float64(3)},
			want:   map[string]string{"Mcp-Param-Attempts": "3"},
		},
		{
			name:   "negative integer is mirrored",
			schema: objSchema(map[string]any{"offset": annotated("integer", "Offset")}),
			args:   map[string]any{"offset": float64(-7)},
			want:   map[string]string{"Mcp-Param-Offset": "-7"},
		},
		{
			name:   "integer arriving as a Go int is mirrored",
			schema: objSchema(map[string]any{"attempts": annotated("integer", "Attempts")}),
			args:   map[string]any{"attempts": 42},
			want:   map[string]string{"Mcp-Param-Attempts": "42"},
		},
		{
			name:   "integer arriving as json.Number is mirrored",
			schema: objSchema(map[string]any{"attempts": annotated("integer", "Attempts")}),
			args:   map[string]any{"attempts": json.Number("42")},
			want:   map[string]string{"Mcp-Param-Attempts": "42"},
		},
		{
			// An optional designated parameter the caller did not supply has no
			// value to mirror. Omitting the header is correct, not an error.
			name:   "absent designated parameter contributes no header",
			schema: objSchema(map[string]any{"region": annotated("string", "Region")}),
			args:   map[string]any{"query": "select 1"},
		},
		{
			name: "nested designated parameter is resolved through the path",
			schema: objSchema(map[string]any{
				"filter": objSchema(map[string]any{"region": annotated("string", "Region")}),
			}),
			args: map[string]any{"filter": map[string]any{"region": "eu-west1"}},
			want: map[string]string{"Mcp-Param-Region": "eu-west1"},
		},
		{
			name: "nested path with a missing intermediate contributes no header",
			schema: objSchema(map[string]any{
				"filter": objSchema(map[string]any{"region": annotated("string", "Region")}),
			}),
			args: map[string]any{"other": "x"},
		},
		{
			// Combinator segments are structural to the schema and absent from the
			// data, so they are skipped when resolving the value's location.
			name: "combinator segments are dropped when resolving",
			schema: objSchema(map[string]any{
				"target": map[string]any{
					"oneOf": []any{
						objSchema(map[string]any{"zone": annotated("string", "Zone")}),
					},
				},
			}),
			args: map[string]any{"target": map[string]any{"zone": "a"}},
			want: map[string]string{"Mcp-Param-Zone": "a"},
		},
		{
			// An array holds many elements and a header holds one value, so there
			// is no well-defined single value to mirror.
			name: "annotation behind an array element is skipped",
			schema: objSchema(map[string]any{
				"targets": map[string]any{
					"type":  "array",
					"items": objSchema(map[string]any{"zone": annotated("string", "Zone")}),
				},
			}),
			args: map[string]any{"targets": []any{map[string]any{"zone": "a"}}},
		},
		{
			name: "two designated parameters both mirror",
			schema: objSchema(map[string]any{
				"region":   annotated("string", "Region"),
				"priority": annotated("string", "Priority"),
			}),
			args: map[string]any{"region": "eu-west1", "priority": "high"},
			want: map[string]string{
				"Mcp-Param-Region":   "eu-west1",
				"Mcp-Param-Priority": "high",
			},
		},
		{
			name:   "empty args mirrors nothing",
			schema: objSchema(map[string]any{"region": annotated("string", "Region")}),
			args:   map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParamHeadersForSchema(tt.schema, tt.args)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParamHeadersForSchema_UnmirrorableValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		schema      map[string]any
		args        map[string]any
		wantErrPart string
	}{
		{
			// The header-injection case. The value comes from the caller
			// (ultimately a model), so it is untrusted: a CRLF must be refused,
			// not silently stripped, or a caller could forge headers on vMCP's
			// outgoing request.
			name:        "CRLF in a string value is refused",
			schema:      objSchema(map[string]any{"region": annotated("string", "Region")}),
			args:        map[string]any{"region": "eu\r\nX-Evil: 1"},
			wantErrPart: "control character",
		},
		{
			name:        "bare newline in a string value is refused",
			schema:      objSchema(map[string]any{"region": annotated("string", "Region")}),
			args:        map[string]any{"region": "eu\nwest"},
			wantErrPart: "control character",
		},
		{
			name:        "NUL in a string value is refused",
			schema:      objSchema(map[string]any{"region": annotated("string", "Region")}),
			args:        map[string]any{"region": "eu\x00west"},
			wantErrPart: "control character",
		},
		{
			name:        "non-integral value for an integer parameter is refused",
			schema:      objSchema(map[string]any{"attempts": annotated("integer", "Attempts")}),
			args:        map[string]any{"attempts": 1.5},
			wantErrPart: "declared integer",
		},
		{
			// SEP-2243 bounds a mirrored integer to JavaScript's safe range.
			name:        "integer above the safe range is refused",
			schema:      objSchema(map[string]any{"attempts": annotated("integer", "Attempts")}),
			args:        map[string]any{"attempts": float64(maxSafeInteger) * 4},
			wantErrPart: "safe integer range",
		},
		{
			name:        "integer below the safe range is refused",
			schema:      objSchema(map[string]any{"attempts": annotated("integer", "Attempts")}),
			args:        map[string]any{"attempts": float64(-maxSafeInteger) * 4},
			wantErrPart: "safe integer range",
		},
		{
			name:        "type mismatch against the schema is refused",
			schema:      objSchema(map[string]any{"region": annotated("string", "Region")}),
			args:        map[string]any{"region": 42},
			wantErrPart: "declared string",
		},
		{
			name:        "non-boolean for a boolean parameter is refused",
			schema:      objSchema(map[string]any{"dry_run": annotated("boolean", "Dry-Run")}),
			args:        map[string]any{"dry_run": "yes"},
			wantErrPart: "declared boolean",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParamHeadersForSchema(tt.schema, tt.args)
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Contains(t, err.Error(), tt.wantErrPart)
			assert.True(t, errors.Is(err, ErrUnmirrorableValue),
				"a bad VALUE must be distinguishable from a bad annotation: %v", err)
		})
	}
}

// TestParamHeadersForSchema_SafeIntegerBoundary pins the inclusive edge of the
// safe-integer range, the off-by-one a reviewer would reasonably doubt.
func TestParamHeadersForSchema_SafeIntegerBoundary(t *testing.T) {
	t.Parallel()

	schema := objSchema(map[string]any{"n": annotated("integer", "N")})

	got, err := ParamHeadersForSchema(schema, map[string]any{"n": float64(maxSafeInteger)})
	require.NoError(t, err, "MAX_SAFE_INTEGER itself is in range")
	assert.Equal(t, map[string]string{"Mcp-Param-N": "9007199254740991"}, got)

	got, err = ParamHeadersForSchema(schema, map[string]any{"n": float64(-maxSafeInteger)})
	require.NoError(t, err, "-MAX_SAFE_INTEGER is in range")
	assert.Equal(t, map[string]string{"Mcp-Param-N": "-9007199254740991"}, got)
}

// TestParamHeadersForSchema_InvalidAnnotationIsNotAValueError keeps the two error
// classes separable: callers blame the caller's arguments for one and the
// backend's tool definition for the other.
func TestParamHeadersForSchema_InvalidAnnotationIsNotAValueError(t *testing.T) {
	t.Parallel()

	schema := objSchema(map[string]any{"ratio": annotated("number", "Ratio")})

	got, err := ParamHeadersForSchema(schema, map[string]any{"ratio": 1.5})
	require.Error(t, err)
	assert.Nil(t, got)
	assert.False(t, errors.Is(err, ErrUnmirrorableValue),
		"a malformed ANNOTATION must not masquerade as a bad value: %v", err)
}
