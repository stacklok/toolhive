// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// --- Fixture types ---------------------------------------------------------

type flatPrimitives struct {
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Enabled bool   `json:"enabled"`
}

type withSkippedTag struct {
	Visible string `json:"visible"`
	Hidden  string `json:"-"`
}

type noJSONTag struct {
	Visible string
}

type withOmitempty struct {
	Maybe string `json:"maybe,omitempty"`
}

type leafInner struct {
	A string `json:"a"`
	B string `json:"b"`
}

type withPointerStruct struct {
	Inner *leafInner `json:"inner"`
}

type withSliceOfStruct struct {
	Items []leafInner `json:"items"`
}

type withSliceOfPointerStruct struct {
	Items []*leafInner `json:"items"`
}

type withSliceOfPrimitive struct {
	Tags []string `json:"tags"`
}

type withMapStructValue struct {
	ByKey map[string]*leafInner `json:"byKey"`
}

type withMapPrimitiveValue struct {
	Labels map[string]string `json:"labels"`
}

type embedSource struct {
	Foo string `json:"foo"`
}

// withEmbeddedNoInline embeds a struct without an explicit `,inline` tag.
// In Go's encoding/json semantics, anonymous fields with no json tag have
// their exported fields promoted into the parent — equivalent to inline.
type withEmbeddedNoInline struct {
	embedSource
	Bar string `json:"bar"`
}

type withEmbeddedInline struct {
	embedSource `json:",inline"`
	Bar         string `json:"bar"`
}

type withUnexportedField struct {
	Visible string `json:"visible"`
	hidden  string //nolint:unused // exercised by reflection
}

type withDuration struct {
	Wait     time.Duration   `json:"wait"`
	WaitMeta metav1.Duration `json:"waitMeta"`
}

type withRawJSON struct {
	Raw json.RawMessage `json:"raw"`
}

type withThvJSON struct {
	M thvjson.Map `json:"m"`
	A thvjson.Any `json:"a"`
}

type withVmcpDuration struct {
	D vmcpconfig.Duration `json:"d"`
}

type recursive struct {
	Name string     `json:"name"`
	Next *recursive `json:"next,omitempty"`
}

// --- Tests -----------------------------------------------------------------

func TestFlattenJSONLeafFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want []string
	}{
		{
			name: "flat primitives",
			in:   flatPrimitives{},
			want: []string{"count", "enabled", "name"},
		},
		{
			name: "json:\"-\" field is skipped",
			in:   withSkippedTag{},
			want: []string{"visible"},
		},
		{
			name: "missing json tag uses Go field name",
			in:   noJSONTag{},
			want: []string{"Visible"},
		},
		{
			name: "omitempty does not appear in path",
			in:   withOmitempty{},
			want: []string{"maybe"},
		},
		{
			name: "pointer field recurses",
			in:   withPointerStruct{},
			want: []string{"inner.a", "inner.b"},
		},
		{
			name: "slice of struct recurses into element",
			in:   withSliceOfStruct{},
			want: []string{"items.a", "items.b"},
		},
		{
			name: "slice of pointer to struct recurses into element",
			in:   withSliceOfPointerStruct{},
			want: []string{"items.a", "items.b"},
		},
		{
			name: "slice of primitive terminates at slice",
			in:   withSliceOfPrimitive{},
			want: []string{"tags"},
		},
		{
			name: "map with struct value recurses into value type",
			in:   withMapStructValue{},
			want: []string{"byKey.a", "byKey.b"},
		},
		{
			name: "map with primitive value terminates at map",
			in:   withMapPrimitiveValue{},
			want: []string{"labels"},
		},
		{
			name: "embedded struct without ,inline still flattens",
			in:   withEmbeddedNoInline{},
			want: []string{"bar", "foo"},
		},
		{
			name: "embedded struct with ,inline flattens",
			in:   withEmbeddedInline{},
			want: []string{"bar", "foo"},
		},
		{
			name: "unexported field is skipped",
			in:   withUnexportedField{},
			want: []string{"visible"},
		},
		{
			name: "time.Duration and metav1.Duration are leaves",
			in:   withDuration{},
			want: []string{"wait", "waitMeta"},
		},
		{
			name: "json.RawMessage is a leaf",
			in:   withRawJSON{},
			want: []string{"raw"},
		},
		{
			name: "thvjson Map and Any are leaves",
			in:   withThvJSON{},
			want: []string{"a", "m"},
		},
		{
			name: "vmcp config Duration is a leaf",
			in:   withVmcpDuration{},
			want: []string{"d"},
		},
		{
			name: "self-referential type does not recurse infinitely",
			in:   recursive{},
			want: []string{"name", "next.name"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FlattenJSONLeafFields(reflect.TypeOf(tc.in))
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFlattenJSONLeafFields_PointerInputDereferenced(t *testing.T) {
	t.Parallel()

	got := FlattenJSONLeafFields(reflect.TypeOf(&flatPrimitives{}))
	require.Equal(t, []string{"count", "enabled", "name"}, got)
}

func TestFlattenJSONLeafFields_OutputIsSorted(t *testing.T) {
	t.Parallel()

	got := FlattenJSONLeafFields(reflect.TypeOf(flatPrimitives{}))
	require.NotEmpty(t, got)
	require.True(t, sort.StringsAreSorted(got), "expected sorted output, got %v", got)
}

func TestFlattenJSONLeafFields_NilOrNonStructReturnsEmpty(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   reflect.Type
	}{
		{name: "nil reflect.Type", in: nil},
		{name: "primitive int", in: reflect.TypeOf(0)},
		{name: "string", in: reflect.TypeOf("")},
		{name: "slice", in: reflect.TypeOf([]string{})},
		{name: "pointer to primitive", in: reflect.TypeOf((*int)(nil))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FlattenJSONLeafFields(tc.in)
			assert.Empty(t, got)
		})
	}
}

func TestFlattenJSONLeafFields_SkipsObjectMetaAndTypeMeta(t *testing.T) {
	t.Parallel()

	type withK8sMeta struct {
		metav1.TypeMeta   `json:",inline"`
		metav1.ObjectMeta `json:"metadata,omitempty"`
		Spec              flatPrimitives `json:"spec"`
	}

	got := FlattenJSONLeafFields(reflect.TypeOf(withK8sMeta{}))
	// Should only contain the spec.* fields; nothing from TypeMeta/ObjectMeta.
	require.Equal(t, []string{"spec.count", "spec.enabled", "spec.name"}, got)
}
