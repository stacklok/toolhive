// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	embedSource `json:",inline"` //nolint:revive // inline is a valid kubernetes json tag option
	Bar         string           `json:"bar"`
}

type withUnexportedField struct {
	Visible string `json:"visible"`
	hidden  string //nolint:unused // exercised by reflection
}

// withDuration covers two leaf paths in one fixture: a primitive int64
// (time.Duration, default branch) and a json.Marshaler (metav1.Duration,
// short-circuit branch). Other Marshaler types follow the same code path.
type withDuration struct {
	Wait     time.Duration   `json:"wait"`
	WaitMeta metav1.Duration `json:"waitMeta"`
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
			// time.Duration is a primitive int64 (default branch);
			// metav1.Duration is a json.Marshaler (short-circuit branch).
			// Together these exercise both leaf-emission paths.
			name: "primitive and json.Marshaler types are leaves",
			in:   withDuration{},
			want: []string{"wait", "waitMeta"},
		},
		{
			name: "self-referential type stops on revisit",
			in:   recursive{},
			want: []string{"name"},
		},
		{
			name: "pointer input is dereferenced",
			in:   &flatPrimitives{},
			want: []string{"count", "enabled", "name"},
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
		metav1.TypeMeta   `json:",inline"` //nolint:revive // inline is a valid kubernetes json tag option
		metav1.ObjectMeta `json:"metadata,omitempty"`
		Spec              flatPrimitives `json:"spec"`
	}

	got := FlattenJSONLeafFields(reflect.TypeOf(withK8sMeta{}))
	// Should only contain the spec.* fields; nothing from TypeMeta/ObjectMeta.
	require.Equal(t, []string{"spec.count", "spec.enabled", "spec.name"}, got)
}
