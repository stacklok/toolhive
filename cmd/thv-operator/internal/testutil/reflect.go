// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package testutil provides reflection-based helpers used by drift-detection
// tests in the operator. It is intended for test code only.
package testutil

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FlattenJSONLeafFields returns every leaf JSON field path under t as a sorted
// slice of dot-delimited paths (e.g. "openTelemetry.tracing.enabled").
//
// A leaf is any type that does not produce nested JSON keys at runtime: a
// primitive, a slice/map of primitives, or a type implementing json.Marshaler
// (whose MarshalJSON shape is opaque to reflection). Structs, slices/maps of
// structs, and pointers to either are recursed into. Field names follow
// encoding/json rules including `,inline` and anonymous-field promotion.
// Self-referential types stop on revisit so the walk always terminates.
//
// If t is nil or, after dereferencing, is not a struct, an empty slice is
// returned rather than panicking.
func FlattenJSONLeafFields(t reflect.Type) []string {
	if t == nil {
		return []string{}
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return []string{}
	}

	leafSet := make(map[string]struct{})
	visited := map[reflect.Type]struct{}{}
	recurseStruct(t, "", leafSet, visited)

	out := make([]string, 0, len(leafSet))
	for p := range leafSet {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// jsonMarshalerType is the reflect.Type of the json.Marshaler interface. Any
// type implementing it (directly or via pointer receiver) produces a JSON
// shape detached from its Go field layout, so it must terminate the walk.
var jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()

// implementsJSONMarshaler reports whether values of t (or *t) have a custom
// MarshalJSON method. The pointer-receiver check matters because Go method
// sets only include pointer-receiver methods when the receiver is addressable.
func implementsJSONMarshaler(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType)
}

// skipFieldTypes lists embedded struct types that must be skipped entirely.
var skipFieldTypes = map[reflect.Type]struct{}{
	reflect.TypeOf(metav1.TypeMeta{}):   {},
	reflect.TypeOf(metav1.ObjectMeta{}): {},
	reflect.TypeOf(metav1.ListMeta{}):   {},
}

// walkStruct recurses into a struct type and adds leaf paths to leafSet.
// prefix is the dot-delimited path accumulated so far (without trailing dot).
// visited is the set of struct types currently on the recursion stack; it is
// used to break cycles on self-referential types. Callers add t to visited
// before invoking this function and remove it afterwards.
func walkStruct(t reflect.Type, prefix string, leafSet map[string]struct{}, visited map[reflect.Type]struct{}) {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		// Skip unexported fields. PkgPath is non-empty for unexported fields.
		// Anonymous (embedded) fields have an empty PkgPath only when their
		// underlying type is exported, so the same check works for them.
		if field.PkgPath != "" && !field.Anonymous {
			continue
		}

		// Skip explicit skip-list types (TypeMeta/ObjectMeta/ListMeta) when
		// embedded.
		if _, skip := skipFieldTypes[field.Type]; skip {
			continue
		}

		name, omit := parseJSONTag(field)
		if omit {
			continue
		}

		// Determine the path segment for this field. Match encoding/json
		// semantics: only anonymous embedded fields are flattened, and only
		// when they have no explicit JSON name. The `,inline` tag option is
		// a Kubernetes documentation idiom — encoding/json itself ignores
		// it, so a NAMED field tagged `json:"foo,inline"` actually marshals
		// as `"foo":{...}` and must NOT be flattened by the walker.
		segment := func() string {
			if field.Anonymous && name == "" {
				return ""
			}
			if name == "" {
				return field.Name
			}
			return name
		}()

		childPrefix := joinPath(prefix, segment)
		walkType(field.Type, childPrefix, leafSet, visited)
	}
}

// walkType walks a single type with the given accumulated prefix. It either
// recurses into nested structs/slices/maps or records a leaf at prefix.
func walkType(t reflect.Type, prefix string, leafSet map[string]struct{}, visited map[reflect.Type]struct{}) {
	// Deref pointers.
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// Custom JSON marshalers short-circuit any further walking. See
	// jsonMarshalerType for the rationale.
	if implementsJSONMarshaler(t) {
		recordLeaf(prefix, leafSet)
		return
	}

	// Only Struct/Slice/Array/Map require recursion; every other Kind
	// (primitives, interfaces, channels, etc.) is a leaf path captured by
	// the default branch.
	switch t.Kind() { //nolint:exhaustive // every other Kind falls through to the default leaf branch by design
	case reflect.Struct:
		recurseStruct(t, prefix, leafSet, visited)
	case reflect.Slice, reflect.Array:
		elem := t.Elem()
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		// Recurse only when the element is a plain struct (no custom marshaler).
		if elem.Kind() == reflect.Struct && !implementsJSONMarshaler(elem) {
			recurseStruct(elem, prefix, leafSet, visited)
			return
		}
		recordLeaf(prefix, leafSet)
	case reflect.Map:
		val := t.Elem()
		for val.Kind() == reflect.Pointer {
			val = val.Elem()
		}
		if val.Kind() == reflect.Struct && !implementsJSONMarshaler(val) {
			recurseStruct(val, prefix, leafSet, visited)
			return
		}
		recordLeaf(prefix, leafSet)
	default:
		recordLeaf(prefix, leafSet)
	}
}

// recurseStruct descends into a nested struct type with cycle protection.
// visited is the set of struct types currently on the recursion stack; if t
// is already present, the walk stops silently to keep self-referential types
// from looping forever.
func recurseStruct(t reflect.Type, prefix string, leafSet map[string]struct{}, visited map[reflect.Type]struct{}) {
	if _, seen := visited[t]; seen {
		return
	}
	visited[t] = struct{}{}
	defer delete(visited, t)
	walkStruct(t, prefix, leafSet, visited)
}

// parseJSONTag extracts (name, omit) from the json struct tag.
//   - name is the explicit JSON name (empty when not specified).
//   - omit is true when the tag is "-" (field must be skipped entirely).
//
// Other tag options (`,omitempty`, `,inline`, `,string`, ...) are
// intentionally ignored — they don't affect the path enumeration.
func parseJSONTag(field reflect.StructField) (string, bool) {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return "", false
	}
	if tag == "-" {
		return "", true
	}
	name, _, _ := strings.Cut(tag, ",")
	return name, false
}

// joinPath joins prefix and segment with a dot, handling empty segments
// (e.g. inline fields) by returning the prefix unchanged.
func joinPath(prefix, segment string) string {
	if segment == "" {
		return prefix
	}
	if prefix == "" {
		return segment
	}
	return prefix + "." + segment
}

// recordLeaf records prefix as a leaf path. An empty prefix is ignored
// because it would not represent a real field.
func recordLeaf(prefix string, leafSet map[string]struct{}) {
	if prefix == "" {
		return
	}
	leafSet[prefix] = struct{}{}
}
