// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"fmt"

	"golang.org/x/exp/jsonrpc2"
)

// ConvertToJSONRPC2ID converts an interface{} ID to jsonrpc2.ID
func ConvertToJSONRPC2ID(id interface{}) (jsonrpc2.ID, error) {
	if id == nil {
		return jsonrpc2.ID{}, nil
	}

	switch v := id.(type) {
	case string:
		return jsonrpc2.StringID(v), nil
	case int:
		return jsonrpc2.Int64ID(int64(v)), nil
	case int64:
		return jsonrpc2.Int64ID(v), nil
	case float64:
		// JSON numbers are often unmarshaled as float64
		return jsonrpc2.Int64ID(int64(v)), nil
	default:
		return jsonrpc2.ID{}, fmt.Errorf("unsupported ID type: %T", id)
	}
}
