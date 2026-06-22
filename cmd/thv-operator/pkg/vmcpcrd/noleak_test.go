// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcpcrd_test

import (
	"reflect"
	"strings"
	"testing"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// internalConfigPkgPath is the import path of the internal runtime config model.
// No type reachable from the CRD spec types may originate here — if one does,
// controller-gen could walk the internal package into the generated CRD schema,
// re-coupling the two. This is the categorical no-leak guarantee.
const internalConfigPkgPath = "github.com/stacklok/toolhive/pkg/vmcp/config"

// TestNoInternalConfigLeak walks every type reachable from the CRD spec types
// and fails if any reachable type belongs to pkg/vmcp/config. Unlike the
// JSON-leaf parity test, this walk preserves type identity (PkgPath), which is
// exactly what controller-gen sees when it builds the schema.
func TestNoInternalConfigLeak(t *testing.T) {
	t.Parallel()

	roots := []reflect.Type{
		reflect.TypeOf(mcpv1beta1.VirtualMCPServerSpec{}),
		reflect.TypeOf(mcpv1beta1.VirtualMCPCompositeToolDefinitionSpec{}),
	}

	for _, root := range roots {
		root := root
		t.Run(root.Name(), func(t *testing.T) {
			t.Parallel()
			w := &leakWalker{visited: map[reflect.Type]struct{}{}, t: t}
			w.walk(root, root.Name())
		})
	}
}

// leakWalker performs a depth-first walk over a type graph, dereferencing
// pointers/slices/arrays/maps and recursing into struct fields. It skips
// metav1 and other apimachinery types (which legitimately live outside our
// control) and breaks cycles via the visited set.
type leakWalker struct {
	visited map[reflect.Type]struct{}
	t       *testing.T
}

func (w *leakWalker) walk(t reflect.Type, path string) {
	if t == nil {
		return
	}

	// Unwrap pointer/container kinds to reach the underlying named type. Each
	// unwrap re-enters walk so the element type is checked for the leak too.
	switch t.Kind() { //nolint:exhaustive // only container kinds need unwrapping; all others fall through to the named-type check below

	case reflect.Pointer, reflect.Slice, reflect.Array:
		w.walk(t.Elem(), path+"[]")
		return
	case reflect.Map:
		w.walk(t.Key(), path+"{key}")
		w.walk(t.Elem(), path+"{val}")
		return
	default:
	}

	// Named types: check the originating package first. This is the actual
	// assertion — a field whose type lives in pkg/vmcp/config is a leak.
	if t.PkgPath() == internalConfigPkgPath {
		w.t.Errorf(
			"no-leak violation: %s has type %s.%s from the internal config package %q.\n"+
				"CRD spec types must reference only the operator-owned vmcpcrd mirror so controller-gen "+
				"cannot walk pkg/vmcp/config into the generated schema.",
			path, t.PkgPath(), t.Name(), internalConfigPkgPath,
		)
	}

	// Skip apimachinery / k8s.io types: they are not ours to police and pull in
	// large, cyclic graphs (e.g. runtime.RawExtension).
	if isSkippedPkg(t.PkgPath()) {
		return
	}

	if t.Kind() != reflect.Struct {
		return
	}

	// Cycle break: only struct types are tracked since only they recurse into
	// fields. Mark before descending, leave marked (a re-visit anywhere in the
	// graph is safe to prune — the type was already fully checked).
	if _, seen := w.visited[t]; seen {
		return
	}
	w.visited[t] = struct{}{}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		w.walk(field.Type, path+"."+field.Name)
	}
}

// isSkippedPkg reports whether a package path belongs to Kubernetes
// apimachinery / k8s.io machinery that we deliberately do not recurse into.
// pkg/vmcp/config is intentionally NOT skipped — it is the package we are
// asserting against.
func isSkippedPkg(pkgPath string) bool {
	if pkgPath == "" {
		return false // builtins / unnamed types: keep walking their structure
	}
	return strings.HasPrefix(pkgPath, "k8s.io/") ||
		strings.HasPrefix(pkgPath, "sigs.k8s.io/")
}
