// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import "context"

// ToolAnnotations holds MCP tool annotation hints that inform authorization
// decisions. The fields match the MCP specification's tool annotation schema.
// Pointer types are used so callers can distinguish "not set" (nil) from
// an explicit false value.
//
// # Trust Boundary
//
// Annotations MUST be sourced from the server-side tool registry (the MCP
// tools/list response), NOT from the client's tools/call request body.
// Allowing clients to supply their own annotations would let a malicious
// caller set readOnlyHint=true on a destructive tool and bypass policies
// that rely on annotations.
//
// # Authorizer Exposure Paths
//
// The two authorizer implementations expose annotations at different
// locations so that policy authors can reference them:
//
//   - Cedar authorizer: flat on the resource entity — e.g. resource.readOnlyHint
//   - HTTP PDP authorizer: nested in the PORC context — context.mcp.annotations.readOnlyHint
type ToolAnnotations struct {
	ReadOnlyHint    *bool `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool `json:"openWorldHint,omitempty"`
}

// toolAnnotationsKey is the unexported context key used by
// WithToolAnnotations / ToolAnnotationsFromContext.
type toolAnnotationsKey struct{}

// WithToolAnnotations stores tool annotations in the given context.
func WithToolAnnotations(ctx context.Context, annotations *ToolAnnotations) context.Context {
	return context.WithValue(ctx, toolAnnotationsKey{}, annotations)
}

// ToolAnnotationsFromContext retrieves tool annotations previously stored with
// WithToolAnnotations. It returns nil when no annotations are present.
func ToolAnnotationsFromContext(ctx context.Context) *ToolAnnotations {
	v, _ := ctx.Value(toolAnnotationsKey{}).(*ToolAnnotations)
	return v
}

// AnnotationsToMap converts non-nil annotation fields to a flat map suitable
// for merging into Cedar resource attributes or HTTP PDP context. Returns nil
// when annotations is nil. Returns an empty (non-nil) map when annotations is
// non-nil but all fields are nil pointers.
func AnnotationsToMap(annotations *ToolAnnotations) map[string]interface{} {
	if annotations == nil {
		return nil
	}

	m := make(map[string]interface{})
	if annotations.ReadOnlyHint != nil {
		m["readOnlyHint"] = *annotations.ReadOnlyHint
	}
	if annotations.DestructiveHint != nil {
		m["destructiveHint"] = *annotations.DestructiveHint
	}
	if annotations.IdempotentHint != nil {
		m["idempotentHint"] = *annotations.IdempotentHint
	}
	if annotations.OpenWorldHint != nil {
		m["openWorldHint"] = *annotations.OpenWorldHint
	}
	return m
}
