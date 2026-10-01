// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package claims provides shared helpers for decoding JWT/OIDC claim values.
package claims

import (
	"errors"
	"fmt"
)

// DecodeStringArray decodes a claim containing []string or []any whose elements
// are all strings. Nil values (including typed nil slices such as []string(nil)
// or []any(nil)), other types, and non-string elements return an error. A
// non-nil empty array returns (nil, nil). The result does not alias the input.
//
// Typed nil slices are rejected rather than treated as empty arrays so a
// security-relevant caller can rely on a consistent nil-is-error contract: an
// absent or malformed claim must never be silently equivalent to a legitimate
// empty-array claim.
func DecodeStringArray(raw any) ([]string, error) {
	if raw == nil {
		return nil, errors.New("claim value is nil")
	}
	switch v := raw.(type) {
	case []string:
		if v == nil {
			return nil, errors.New("claim value is nil")
		}
		return append([]string(nil), v...), nil
	case []any:
		if v == nil {
			return nil, errors.New("claim value is nil")
		}
		if len(v) == 0 {
			return nil, nil
		}
		out := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("array element at index %d is %T, want string", i, item)
			}
			out[i] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("value is %T, want JSON string array", raw)
	}
}
