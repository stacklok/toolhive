// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package conversions provides bidirectional type conversion between Go
// values and Starlark values, MCP result parsing, and tool name sanitization.
package conversions

import (
	"encoding/json"
	"fmt"
	"math"

	"go.starlark.net/starlark"
)

// GoToStarlark converts a Go value to a Starlark value.
//
//nolint:gocyclo // type switch over Go types is inherently branchy
func GoToStarlark(v interface{}) (starlark.Value, error) {
	switch v := v.(type) {
	case nil:
		return starlark.None, nil
	case bool:
		return starlark.Bool(v), nil
	case int:
		return starlark.MakeInt(v), nil
	case int64:
		return starlark.MakeInt64(v), nil
	case float64:
		return goFloat64ToStarlark(v), nil
	case string:
		return starlark.String(v), nil
	case []interface{}:
		return goSliceToStarlark(v)
	case map[string]interface{}:
		return goMapToStarlark(v)
	case json.Number:
		return goJSONNumberToStarlark(v)
	default:
		return nil, fmt.Errorf("unsupported Go type %T for Starlark conversion", v)
	}
}

// StarlarkToGo converts a Starlark value to a Go value.
func StarlarkToGo(v starlark.Value) interface{} {
	switch v := v.(type) {
	case starlark.NoneType:
		return nil
	case starlark.Bool:
		return bool(v)
	case starlark.Int:
		if i, ok := v.Int64(); ok {
			return i
		}
		return v.String()
	case starlark.Float:
		return float64(v)
	case starlark.String:
		return string(v)
	case *starlark.List:
		result := make([]interface{}, v.Len())
		for i := range v.Len() {
			result[i] = StarlarkToGo(v.Index(i))
		}
		return result
	case *starlark.Dict:
		result := make(map[string]interface{})
		for _, item := range v.Items() {
			key := StarlarkToGo(item[0])
			keyStr, ok := key.(string)
			if !ok {
				keyStr = fmt.Sprintf("%v", key)
			}
			result[keyStr] = StarlarkToGo(item[1])
		}
		return result
	case starlark.Tuple:
		result := make([]interface{}, len(v))
		for i, elem := range v {
			result[i] = StarlarkToGo(elem)
		}
		return result
	default:
		return v.String()
	}
}

// goFloat64ToStarlark converts a float64 to a Starlark value, promoting
// whole numbers to Int for JSON number fidelity.
func goFloat64ToStarlark(v float64) starlark.Value {
	if v == math.Trunc(v) && !math.IsInf(v, 0) && !math.IsNaN(v) && math.Abs(v) < (1<<53) {
		return starlark.MakeInt64(int64(v))
	}
	return starlark.Float(v)
}

func goSliceToStarlark(v []interface{}) (starlark.Value, error) {
	elems := make([]starlark.Value, len(v))
	for i, e := range v {
		sv, err := GoToStarlark(e)
		if err != nil {
			return nil, err
		}
		elems[i] = sv
	}
	return starlark.NewList(elems), nil
}

func goMapToStarlark(v map[string]interface{}) (starlark.Value, error) {
	d := starlark.NewDict(len(v))
	for k, val := range v {
		sv, err := GoToStarlark(val)
		if err != nil {
			return nil, err
		}
		if err := d.SetKey(starlark.String(k), sv); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func goJSONNumberToStarlark(v json.Number) (starlark.Value, error) {
	if i, err := v.Int64(); err == nil {
		return starlark.MakeInt64(i), nil
	}
	if f, err := v.Float64(); err == nil {
		return starlark.Float(f), nil
	}
	return starlark.String(v.String()), nil
}
