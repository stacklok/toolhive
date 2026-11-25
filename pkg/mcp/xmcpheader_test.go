// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// objSchema builds an object inputSchema with the given properties, the shape
// every tool inputSchema takes at its root.
func objSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props}
}

// annotated builds a leaf parameter schema of the given type carrying an
// x-mcp-header annotation.
func annotated(typ, header string) map[string]any {
	return map[string]any{"type": typ, XMCPHeaderAnnotation: header}
}

func TestParamHeaders_Accepted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema map[string]any
		want   []ParamHeader
	}{
		{
			name:   "nil schema has no annotations",
			schema: nil,
		},
		{
			name:   "empty schema has no annotations",
			schema: map[string]any{},
		},
		{
			// The overwhelmingly common case: SEP-2243 makes the annotation
			// optional for servers, so almost every real tool has none.
			name: "unannotated schema has no annotations",
			schema: objSchema(map[string]any{
				"query": map[string]any{"type": "string"},
			}),
		},
		{
			// The SEP's own worked example (execute_sql / Region).
			name: "single string annotation",
			schema: objSchema(map[string]any{
				"region": annotated("string", "Region"),
				"query":  map[string]any{"type": "string"},
			}),
			want: []ParamHeader{{Path: []string{"region"}, Name: "Region", Type: "string"}},
		},
		{
			name: "integer and boolean are permitted primitives",
			schema: objSchema(map[string]any{
				"attempts": annotated("integer", "Attempts"),
				"dry_run":  annotated("boolean", "Dry-Run"),
			}),
			want: []ParamHeader{
				{Path: []string{"attempts"}, Name: "Attempts", Type: "integer"},
				{Path: []string{"dry_run"}, Name: "Dry-Run", Type: "boolean"},
			},
		},
		{
			// SEP-2243: "These annotations can be applied to properties at any
			// nesting depth."
			name: "annotation nested inside an object property",
			schema: objSchema(map[string]any{
				"filter": objSchema(map[string]any{
					"region": annotated("string", "Region"),
				}),
			}),
			want: []ParamHeader{{Path: []string{"filter", "region"}, Name: "Region", Type: "string"}},
		},
		{
			name: "annotation inside array items",
			schema: objSchema(map[string]any{
				"targets": map[string]any{
					"type":  "array",
					"items": objSchema(map[string]any{"zone": annotated("string", "Zone")}),
				},
			}),
			want: []ParamHeader{{Path: []string{"targets", "[]", "zone"}, Name: "Zone", Type: "string"}},
		},
		{
			// oneOf/anyOf/allOf carry real schemas in this repo's tool sets --
			// #5976 fixed ingestion dropping them -- so the walk must descend them
			// or an annotation inside a combinator branch would go unvalidated.
			name: "annotation inside a oneOf branch",
			schema: objSchema(map[string]any{
				"target": map[string]any{
					"oneOf": []any{
						map[string]any{"type": "string"},
						objSchema(map[string]any{"zone": annotated("string", "Zone")}),
					},
				},
			}),
			want: []ParamHeader{{Path: []string{"target", "oneOf[1]", "zone"}, Name: "Zone", Type: "string"}},
		},
		{
			// Distinct spellings that do not collide case-insensitively are fine.
			name: "two distinct annotations",
			schema: objSchema(map[string]any{
				"region":   annotated("string", "Region"),
				"priority": annotated("string", "Priority"),
			}),
			want: []ParamHeader{
				{Path: []string{"priority"}, Name: "Priority", Type: "string"},
				{Path: []string{"region"}, Name: "Region", Type: "string"},
			},
		},
		{
			// An annotation on the root designates no parameter, so it is ignored
			// rather than rejected -- the root IS the parameter object.
			name: "annotation on the root node is ignored",
			schema: map[string]any{
				"type":               "object",
				XMCPHeaderAnnotation: "Bogus",
				"properties":         map[string]any{"query": map[string]any{"type": "string"}},
			},
		},
		{
			// All tchar punctuation is legal in an HTTP field name.
			name: "tchar punctuation is a valid token",
			schema: objSchema(map[string]any{
				"weird": annotated("string", "A!#$%&'*+-.^_`|~9z"),
			}),
			want: []ParamHeader{{Path: []string{"weird"}, Name: "A!#$%&'*+-.^_`|~9z", Type: "string"}},
		},
		{
			// A non-schema value where a schema is expected must be skipped, not
			// panicked on: the schema is backend-supplied and need not be sane.
			name: "non-object property value is skipped",
			schema: objSchema(map[string]any{
				"bogus":  "not a schema",
				"region": annotated("string", "Region"),
			}),
			want: []ParamHeader{{Path: []string{"region"}, Name: "Region", Type: "string"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParamHeaders(tt.schema)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			// ValidateParamHeaders is ParamHeaders with the result discarded, so
			// it must agree on every accepted schema.
			assert.NoError(t, ValidateParamHeaders(tt.schema))
		})
	}
}

func TestParamHeaders_Rejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		schema      map[string]any
		wantErrPart string
	}{
		{
			name:        "empty annotation value",
			schema:      objSchema(map[string]any{"region": annotated("string", "")}),
			wantErrPart: "must not be empty",
		},
		{
			name: "non-string annotation value",
			schema: objSchema(map[string]any{
				"region": map[string]any{"type": "string", XMCPHeaderAnnotation: 42},
			}),
			wantErrPart: "must be a string",
		},
		{
			// A space is not a tchar; allowing it would let a backend inject a
			// second header or a request line into the outgoing request.
			name:        "annotation containing a space",
			schema:      objSchema(map[string]any{"region": annotated("string", "My Region")}),
			wantErrPart: "not a valid HTTP field-name token",
		},
		{
			// CRLF is the header-injection vector specifically.
			name:        "annotation containing CRLF",
			schema:      objSchema(map[string]any{"region": annotated("string", "R\r\nX: y")}),
			wantErrPart: "not a valid HTTP field-name token",
		},
		{
			name:        "annotation containing a colon",
			schema:      objSchema(map[string]any{"region": annotated("string", "R:egion")}),
			wantErrPart: "not a valid HTTP field-name token",
		},
		{
			// SEP-2243 excludes number explicitly: a float has no canonical wire
			// spelling, so mirroring it would be lossy.
			name:        "number is not a permitted type",
			schema:      objSchema(map[string]any{"ratio": annotated("number", "Ratio")}),
			wantErrPart: `not permitted on type "number"`,
		},
		{
			name:        "object is not a permitted type",
			schema:      objSchema(map[string]any{"blob": annotated("object", "Blob")}),
			wantErrPart: `not permitted on type "object"`,
		},
		{
			name:        "array is not a permitted type",
			schema:      objSchema(map[string]any{"list": annotated("array", "List")}),
			wantErrPart: `not permitted on type "array"`,
		},
		{
			name: "missing type is not a declared primitive",
			schema: objSchema(map[string]any{
				"region": map[string]any{XMCPHeaderAnnotation: "Region"},
			}),
			wantErrPart: "requires a declared primitive type",
		},
		{
			// A union type is not a single declared primitive, so mirroring cannot
			// know the spelling.
			name: "union type is not a declared primitive",
			schema: objSchema(map[string]any{
				"region": map[string]any{
					"type":               []any{"string", "null"},
					XMCPHeaderAnnotation: "Region",
				},
			}),
			wantErrPart: "requires a declared primitive type",
		},
		{
			name: "exact duplicate annotation",
			schema: objSchema(map[string]any{
				"a": annotated("string", "Region"),
				"b": annotated("string", "Region"),
			}),
			wantErrPart: "collides case-insensitively",
		},
		{
			// HTTP field names are case-insensitive, so these two would mirror onto
			// the same header and one would silently win.
			name: "case-insensitive duplicate annotation",
			schema: objSchema(map[string]any{
				"a": annotated("string", "Region"),
				"b": annotated("string", "REGION"),
			}),
			wantErrPart: "collides case-insensitively",
		},
		{
			// Uniqueness is scoped to the whole inputSchema, not to one object
			// level, so a nested collision must also be caught.
			name: "duplicate across nesting levels",
			schema: objSchema(map[string]any{
				"region": annotated("string", "Region"),
				"filter": objSchema(map[string]any{"r": annotated("string", "region")}),
			}),
			wantErrPart: "collides case-insensitively",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParamHeaders(tt.schema)
			require.Error(t, err)
			assert.Nil(t, got, "a rejected schema must yield no annotations")
			assert.Contains(t, err.Error(), tt.wantErrPart)
			// ValidateParamHeaders must agree on every rejected schema too.
			assert.Error(t, ValidateParamHeaders(tt.schema))
		})
	}
}

func TestParamHeaders_DepthLimit(t *testing.T) {
	t.Parallel()

	// Nest well past maxSchemaDepth. A backend can advertise any tool list it
	// likes, so an unbounded recursive walk is a stack-exhaustion vector.
	deep := map[string]any{"type": "string", XMCPHeaderAnnotation: "Deep"}
	for range maxSchemaDepth + 10 {
		deep = objSchema(map[string]any{"next": deep})
	}

	got, err := ParamHeaders(deep)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.True(t, errors.Is(err, ErrSchemaTooDeep), "want ErrSchemaTooDeep, got %v", err)
}

func TestParamHeaders_ShallowNestingIsNotRejected(t *testing.T) {
	t.Parallel()

	// The depth cap must not reject legitimately nested schemas. Build one just
	// inside the limit and confirm the annotation is still found, so a future
	// change to maxSchemaDepth that makes the walk too strict fails here.
	const depth = maxSchemaDepth - 2
	schema := map[string]any{"type": "string", XMCPHeaderAnnotation: "Deep"}
	for range depth {
		schema = objSchema(map[string]any{"next": schema})
	}

	got, err := ParamHeaders(schema)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Deep", got[0].Name)
	assert.Len(t, got[0].Path, depth)
}

func TestParamHeader_HeaderName(t *testing.T) {
	t.Parallel()

	// The annotation value is the header's SUFFIX; the wire name carries the
	// Mcp-Param- prefix. Conflating the two is the easy mistake for a caller.
	p := ParamHeader{Path: []string{"region"}, Name: "Region", Type: "string"}
	assert.Equal(t, "Mcp-Param-Region", p.HeaderName())
	assert.True(t, strings.HasPrefix(p.HeaderName(), ParamHeaderPrefix))
}

func TestParamHeaders_SiblingPathsAreIndependent(t *testing.T) {
	t.Parallel()

	// Guards childPath: appending to the shared path slice would let one branch's
	// captured path be overwritten by the next sibling's traversal, silently
	// mirroring a value onto the wrong parameter's header.
	schema := objSchema(map[string]any{
		"first":  objSchema(map[string]any{"alpha": annotated("string", "Alpha")}),
		"second": objSchema(map[string]any{"beta": annotated("string", "Beta")}),
	})

	got, err := ParamHeaders(schema)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"first", "alpha"}, got[0].Path)
	assert.Equal(t, []string{"second", "beta"}, got[1].Path)
}
