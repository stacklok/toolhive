// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"reflect"
	"slices"
)

// isNilValue reports whether v is nil: a nil interface, or a nil pointer, map,
// slice, func, chan or interface of a concrete type. A plain v == nil misses
// the typed cases, and calling through them panics.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	nilable := []reflect.Kind{reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface}
	return slices.Contains(nilable, rv.Kind()) && rv.IsNil()
}
