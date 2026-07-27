// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// SEP-2243 ("HTTP standardization") lets an MCP server mark individual tool
// parameters for mirroring into HTTP request headers, via an x-mcp-header
// annotation inside the parameter's schema in the tool's inputSchema. A server
// MAY use the annotation; a client MUST honour it, sending each designated
// parameter's value as the header Mcp-Param-{name} on the tools/call request. A
// server that designated a parameter and did not receive its header rejects the
// call with -32020.
//
// This file owns the annotation's vocabulary and its validation. It is
// deliberately the single place that reads x-mcp-header, so the two consumers --
// ingestion-time validation of a backend's advertised tools, and call-time
// derivation of the outgoing headers -- cannot drift in their reading of the
// constraints. (Compare the "third independent copy" problem called out for the
// reserved _meta keys in #5986.)
const (
	// XMCPHeaderAnnotation is the JSON Schema extension key a server sets on a
	// tool parameter to designate it for header mirroring. Its value is the
	// header's name suffix, NOT the full header name.
	XMCPHeaderAnnotation = "x-mcp-header"

	// ParamHeaderPrefix prefixes every mirrored parameter header: an annotation
	// of "Region" is sent as "Mcp-Param-Region".
	ParamHeaderPrefix = "Mcp-Param-"
)

// maxSchemaDepth bounds the inputSchema traversal. Annotations are legal at any
// nesting depth, so the walk has to recurse, and the schema is attacker-supplied
// from vMCP's perspective (a backend can advertise any tool list it likes). A
// depth cap keeps a hostile or accidentally-recursive schema from exhausting the
// stack. 64 is far past any hand-written tool schema.
const maxSchemaDepth = 64

// ErrSchemaTooDeep is returned when an inputSchema nests past maxSchemaDepth.
// It is deliberately distinguishable from a constraint violation: the schema may
// be perfectly valid and merely beyond what this walk will inspect, so a caller
// that wants to fail open on depth alone can single it out.
var ErrSchemaTooDeep = errors.New("tool inputSchema exceeds maximum inspection depth")

// ParamHeader is one x-mcp-header annotation found in a tool's inputSchema.
type ParamHeader struct {
	// Path is the property path from the root of inputSchema to the annotated
	// parameter, e.g. ["filter", "region"] for a nested property. Length is
	// always >= 1.
	Path []string

	// Name is the annotation's value -- the suffix appended to
	// ParamHeaderPrefix, not the full header name. Use HeaderName for that.
	Name string

	// Type is the annotated parameter's declared JSON Schema type, guaranteed by
	// validation to be one of "string", "integer", or "boolean".
	Type string
}

// HeaderName is the full HTTP header name this annotation mirrors into.
func (p ParamHeader) HeaderName() string {
	return ParamHeaderPrefix + p.Name
}

// ParamHeaders walks a tool's inputSchema and returns every x-mcp-header
// annotation it declares, in a deterministic order (by path). A schema with no
// annotations -- the overwhelmingly common case -- returns nil, nil.
//
// It returns an error when the schema violates any of SEP-2243's constraints on
// the annotation:
//
//   - the value must be a non-empty string;
//   - it must be a valid HTTP field-name token (RFC 9110 tchar), which also
//     excludes control characters and whitespace;
//   - it must be unique, case-insensitively, within the whole inputSchema
//     (HTTP field names are case-insensitive, so two spellings would collide on
//     the wire);
//   - it may only annotate a parameter whose declared type is "string",
//     "integer", or "boolean" -- notably NOT "number", which SEP-2243 excludes
//     because a float has no canonical wire spelling.
//
// A missing or non-string "type" on an annotated parameter is treated as a
// violation. SEP-2243 permits the annotation only on primitive parameters, and
// an undeclared type is not a declared primitive; equally, mirroring cannot
// serialize a value whose type it does not know. This is the strict reading:
// it rejects the tool rather than guessing a spelling for the header value.
//
// Traversal covers "properties", array "items", and the "oneOf"/"anyOf"/"allOf"
// combinators, so an annotation is found wherever SEP-2243 allows one. Nesting
// past maxSchemaDepth yields ErrSchemaTooDeep.
func ParamHeaders(schema map[string]any) ([]ParamHeader, error) {
	if len(schema) == 0 {
		return nil, nil
	}
	// seen maps the case-folded annotation value to the path that first claimed
	// it, so a duplicate can name both colliding parameters in its error.
	seen := map[string]string{}
	var found []ParamHeader
	if err := walkSchema(schema, nil, 0, seen, &found); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	// Deterministic order so callers (and tests) see a stable sequence
	// regardless of Go's map iteration order.
	slices.SortFunc(found, func(a, b ParamHeader) int {
		return strings.Compare(strings.Join(a.Path, "."), strings.Join(b.Path, "."))
	})
	return found, nil
}

// ValidateParamHeaders reports whether a tool's inputSchema declares only
// SEP-2243-conformant x-mcp-header annotations. It is ParamHeaders with the
// annotations discarded, for the ingestion-time check where only the verdict
// matters.
func ValidateParamHeaders(schema map[string]any) error {
	_, err := ParamHeaders(schema)
	return err
}

// walkSchema recurses one schema node, appending any annotation it finds to
// found. path is the property path to this node ("" at the root, which cannot
// itself be annotated -- an annotation designates a parameter, and the root is
// the parameter object).
func walkSchema(node map[string]any, path []string, depth int, seen map[string]string, found *[]ParamHeader) error {
	if depth > maxSchemaDepth {
		return fmt.Errorf("%w (%d)", ErrSchemaTooDeep, maxSchemaDepth)
	}

	// An annotation on the root node designates no parameter, so it is ignored
	// rather than treated as an error: len(path) == 0 only at the root.
	if len(path) > 0 {
		if raw, ok := node[XMCPHeaderAnnotation]; ok {
			hdr, err := parseAnnotation(raw, node, path, seen)
			if err != nil {
				return err
			}
			*found = append(*found, hdr)
		}
	}

	return walkChildren(node, path, depth, seen, found)
}

// walkChildren recurses into every sub-schema of node that SEP-2243 allows an
// annotation to appear in: object properties, array element schemas, and the
// oneOf/anyOf/allOf combinator branches. Split from walkSchema to keep each
// within the cyclomatic limit.
func walkChildren(node map[string]any, path []string, depth int, seen map[string]string, found *[]ParamHeader) error {
	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			subSchema, ok := sub.(map[string]any)
			if !ok {
				continue // not a schema object; nothing to inspect
			}
			if err := walkSchema(subSchema, childPath(path, name), depth+1, seen, found); err != nil {
				return err
			}
		}
	}

	// Array element schemas: "items" is a schema object in the JSON Schema
	// dialect MCP uses. The element carries no property name of its own, so it
	// inherits the array's path with an "[]" marker for legibility in errors.
	if items, ok := node["items"].(map[string]any); ok {
		if err := walkSchema(items, childPath(path, "[]"), depth+1, seen, found); err != nil {
			return err
		}
	}

	return walkCombinators(node, path, depth, seen, found)
}

// walkCombinators recurses into the oneOf/anyOf/allOf branches of node. These
// carry real schemas in this repo's backend tool sets (#5976 fixed ingestion
// dropping them), so an annotation inside a branch must be validated like any
// other.
func walkCombinators(
	node map[string]any, path []string, depth int, seen map[string]string, found *[]ParamHeader,
) error {
	for _, combinator := range []string{"oneOf", "anyOf", "allOf"} {
		branches, ok := node[combinator].([]any)
		if !ok {
			continue
		}
		for i, branch := range branches {
			branchSchema, ok := branch.(map[string]any)
			if !ok {
				continue
			}
			marker := fmt.Sprintf("%s[%d]", combinator, i)
			if err := walkSchema(branchSchema, childPath(path, marker), depth+1, seen, found); err != nil {
				return err
			}
		}
	}
	return nil
}

// childPath returns path with seg appended, always in freshly allocated storage.
// Appending to path directly would let sibling recursions share (and overwrite)
// one backing array, so a path captured deeper in the walk could be rewritten by
// the next sibling. Allocating per child keeps every captured path independent.
func childPath(path []string, seg string) []string {
	child := make([]string, len(path)+1)
	copy(child, path)
	child[len(path)] = seg
	return child
}

// parseAnnotation validates a single x-mcp-header annotation against SEP-2243
// and records it in seen for the uniqueness check.
func parseAnnotation(raw any, node map[string]any, path []string, seen map[string]string) (ParamHeader, error) {
	where := strings.Join(path, ".")

	name, ok := raw.(string)
	if !ok {
		return ParamHeader{}, fmt.Errorf(
			"parameter %q: %s must be a string, got %T", where, XMCPHeaderAnnotation, raw)
	}
	if name == "" {
		return ParamHeader{}, fmt.Errorf("parameter %q: %s must not be empty", where, XMCPHeaderAnnotation)
	}
	if bad, invalid := firstNonTokenChar(name); invalid {
		return ParamHeader{}, fmt.Errorf(
			"parameter %q: %s value %q is not a valid HTTP field-name token (offending character %q)",
			where, XMCPHeaderAnnotation, name, bad)
	}

	// HTTP field names are case-insensitive, so two annotations differing only in
	// case would mirror onto the same header and one would silently win.
	folded := strings.ToLower(name)
	if first, dup := seen[folded]; dup {
		return ParamHeader{}, fmt.Errorf(
			"%s value %q on parameter %q collides case-insensitively with parameter %q",
			XMCPHeaderAnnotation, name, where, first)
	}
	seen[folded] = where

	declared, ok := node["type"].(string)
	if !ok {
		return ParamHeader{}, fmt.Errorf(
			"parameter %q: %s requires a declared primitive type (string, integer, or boolean)",
			where, XMCPHeaderAnnotation)
	}
	switch declared {
	case "string", "integer", "boolean":
	default:
		return ParamHeader{}, fmt.Errorf(
			"parameter %q: %s is not permitted on type %q (only string, integer, or boolean)",
			where, XMCPHeaderAnnotation, declared)
	}

	// childPath already gives each branch its own storage, so cloning is belt and
	// braces -- it keeps the returned Path independent of the walk even if the
	// traversal's path handling is later changed.
	return ParamHeader{Path: slices.Clone(path), Name: name, Type: declared}, nil
}

// firstNonTokenChar returns the first byte of s that is not an RFC 9110 tchar,
// reporting true when one exists. Operating on bytes rather than runes is
// correct here: every tchar is ASCII, so any multi-byte rune is invalid and its
// leading byte is a faithful thing to name in the error.
func firstNonTokenChar(s string) (string, bool) {
	for i := 0; i < len(s); i++ {
		if !isTokenChar(s[i]) {
			return string(s[i]), true
		}
	}
	return "", false
}

// isTokenChar reports whether c is an RFC 9110 tchar, the character set HTTP
// field names are drawn from.
func isTokenChar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z',
		c >= 'A' && c <= 'Z',
		c >= '0' && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	}
	return false
}
