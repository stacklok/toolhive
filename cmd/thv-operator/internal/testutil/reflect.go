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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

// FlattenJSONLeafFields returns every leaf JSON field path under t as a sorted
// slice of dot-delimited paths (e.g. "openTelemetry.tracing.enabled").
//
// Walking semantics:
//   - For struct fields, the JSON tag's name (before any comma) is used as the
//     path segment. If the tag is "-", the field is skipped. If the tag is
//     missing, the Go field name is used.
//   - Unexported fields are skipped.
//   - Fields with `,inline` flatten into the parent path with no prefix.
//   - Pointer types are dereferenced and walked.
//   - Struct types are recursed into.
//   - Slice and array types whose ELEMENT type is a struct (or pointer to a
//     struct) recurse into that element type, joining with the parent path.
//     A path like "tools.workload" therefore means "every element of the
//     `tools` slice has a `workload` field".
//   - Map types whose VALUE type is a struct (or pointer to a struct) recurse
//     into that value type the same way.
//   - All other types (primitives, []string, map[string]string,
//     time.Duration, metav1.Duration, json.RawMessage, and any type listed
//     in the leaf allowlist below) terminate the walk and contribute one
//     leaf path entry.
//   - To prevent infinite recursion on self-referential types, track visited
//     types per walk and stop if a cycle is detected.
//
// If t is nil or, after dereferencing, is not a struct, an empty slice is
// returned rather than panicking.
func FlattenJSONLeafFields(t reflect.Type) []string {
	if t == nil {
		return []string{}
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return []string{}
	}

	leafSet := make(map[string]struct{})
	visited := map[reflect.Type]int{}
	recurseStruct(t, "", leafSet, visited)

	out := make([]string, 0, len(leafSet))
	for p := range leafSet {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// leafTypes lists types that terminate the walk even though they are structs.
// They contribute a single leaf entry at their parent path.
var leafTypes = map[reflect.Type]struct{}{
	reflect.TypeOf(time.Duration(0)):       {},
	reflect.TypeOf(metav1.Duration{}):      {},
	reflect.TypeOf(json.RawMessage{}):      {},
	reflect.TypeOf(thvjson.Map{}):          {},
	reflect.TypeOf(thvjson.Any{}):          {},
	reflect.TypeOf(vmcpconfig.Duration(0)): {},
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
func walkStruct(t reflect.Type, prefix string, leafSet map[string]struct{}, visited map[reflect.Type]int) {
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

		name, inline, omit := parseJSONTag(field)
		if omit {
			continue
		}

		// Determine the path segment for this field.
		segment := func() string {
			// Anonymous fields are inlined by encoding/json when they have no
			// json name (or an explicit `,inline` tag). Treat them as inline.
			if field.Anonymous && (name == "" || inline) {
				return ""
			}
			if inline {
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
func walkType(t reflect.Type, prefix string, leafSet map[string]struct{}, visited map[reflect.Type]int) {
	// Deref pointers.
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Allow-listed leaf types short-circuit any further walking.
	if _, ok := leafTypes[t]; ok {
		recordLeaf(prefix, leafSet)
		return
	}

	switch t.Kind() {
	case reflect.Struct:
		recurseStruct(t, prefix, leafSet, visited)
	case reflect.Slice, reflect.Array:
		elem := t.Elem()
		for elem.Kind() == reflect.Ptr {
			elem = elem.Elem()
		}
		// Recurse only when the element is a struct that is NOT a leaf type.
		if _, isLeaf := leafTypes[elem]; !isLeaf && elem.Kind() == reflect.Struct {
			recurseStruct(elem, prefix, leafSet, visited)
			return
		}
		recordLeaf(prefix, leafSet)
	case reflect.Map:
		val := t.Elem()
		for val.Kind() == reflect.Ptr {
			val = val.Elem()
		}
		if _, isLeaf := leafTypes[val]; !isLeaf && val.Kind() == reflect.Struct {
			recurseStruct(val, prefix, leafSet, visited)
			return
		}
		recordLeaf(prefix, leafSet)
	default:
		recordLeaf(prefix, leafSet)
	}
}

// maxStructRevisits caps how many times a single struct type may appear on the
// recursion stack. A value of 2 means the type may be entered at most twice
// on the same path: the original entry plus one self-referential visit. This
// is enough for one level of "next.<field>" expansion in cyclic types like
// linked lists, while still guaranteeing the walk terminates.
const maxStructRevisits = 2

// recurseStruct descends into a nested struct type with cycle protection.
// visited holds the count of how many times each struct type currently appears
// on the recursion stack. When entering t would exceed maxStructRevisits, the
// walk stops silently — no leaf is recorded, on the assumption that the
// useful leaves under t have already been captured by the earlier visit on
// the same path.
func recurseStruct(t reflect.Type, prefix string, leafSet map[string]struct{}, visited map[reflect.Type]int) {
	if visited[t] >= maxStructRevisits {
		return
	}
	visited[t]++
	defer func() { visited[t]-- }()
	walkStruct(t, prefix, leafSet, visited)
}

// parseJSONTag extracts (name, inline, omit) from the json struct tag.
//   - name is the explicit JSON name (empty when not specified).
//   - inline is true when the tag includes ",inline".
//   - omit is true when the tag is "-" (field must be skipped entirely).
func parseJSONTag(field reflect.StructField) (string, bool, bool) {
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return "", false, false
	}
	if tag == "-" {
		return "", false, true
	}
	parts := strings.Split(tag, ",")
	name := parts[0]
	inline := false
	for _, p := range parts[1:] {
		if p == "inline" {
			inline = true
		}
	}
	return name, inline, false
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
