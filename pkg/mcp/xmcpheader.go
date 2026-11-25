// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
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

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER (2^53 - 1). SEP-2243
// requires a mirrored integer to sit within this range, since a peer that parses
// the header with JSON-number semantics could not round-trip a larger value.
const maxSafeInteger = 1<<53 - 1

// ErrUnmirrorableValue is returned when a designated parameter's value cannot be
// mirrored into a header: a control character (header injection), a non-integral
// or out-of-safe-range integer, or a value whose type contradicts the schema.
var ErrUnmirrorableValue = errors.New("parameter value cannot be mirrored into an HTTP header")

// MirrorParamHeaders derives the Mcp-Param-* headers to send with a tools/call,
// given the tool's x-mcp-header annotations (from ParamHeaders) and the call's
// arguments. The result maps full header names to values, ready to set on the
// request; it is nil when nothing is to be mirrored.
//
// A designated parameter that is absent from args contributes no header. That is
// deliberate rather than an error: an optional parameter the caller did not
// supply has no value to mirror, and SEP-2243's -32020 covers the server's view
// of a genuinely missing designated value. A provided empty string is instead
// sent as the base64 sentinel so it remains distinguishable from an absent
// header.
//
// Annotations reached through an array element ("[]" in the path) are skipped: an
// array holds many elements and a header holds one value, so there is no
// well-defined single value to send. Combinator segments (oneOf/anyOf/allOf) are
// dropped when resolving, because they are structural to the schema and absent
// from the arguments the schema describes.
//
// It returns an error wrapping ErrUnmirrorableValue when a present value cannot
// be safely rendered. Values originate from the caller (ultimately a model), so
// they are untrusted: a CR, LF, or NUL in a string would let a caller forge
// additional headers on vMCP's outgoing request, and is refused rather than
// silently stripped.
func MirrorParamHeaders(headers []ParamHeader, args map[string]any) (map[string]string, error) {
	if len(headers) == 0 || len(args) == 0 {
		return nil, nil
	}
	var out map[string]string
	for _, h := range headers {
		if slices.Contains(h.Path, "[]") {
			continue
		}
		value, ok := resolveArg(args, h.Path)
		if !ok {
			continue
		}
		rendered, err := renderHeaderValue(h, value)
		if err != nil {
			return nil, err
		}
		if out == nil {
			out = map[string]string{}
		}
		out[h.HeaderName()] = rendered
	}
	return out, nil
}

// ParamHeadersForSchema is ParamHeaders followed by MirrorParamHeaders: it takes
// a tool's inputSchema and a call's arguments and returns the Mcp-Param-* headers
// to send. It exists so the several call sites that mirror headers share one
// reading of the two-step dance rather than each re-deriving it.
//
// A schema error is returned unwrapped from ParamHeaders; a value error wraps
// ErrUnmirrorableValue. Callers distinguish them because the first indicts the
// backend's tool definition and the second the caller's arguments.
func ParamHeadersForSchema(schema map[string]any, args map[string]any) (map[string]string, error) {
	annotations, err := ParamHeaders(schema)
	if err != nil {
		return nil, err
	}
	if len(annotations) == 0 {
		return nil, nil
	}
	return MirrorParamHeaders(annotations, args)
}

// resolveArg walks path through args and returns the value at its end. Segments
// naming a combinator are skipped: they exist in the schema's structure, not in
// the data it describes. A missing or non-object intermediate reports false.
func resolveArg(args map[string]any, path []string) (any, bool) {
	current := any(args)
	for _, seg := range path {
		if isCombinatorSegment(seg) {
			continue
		}
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = obj[seg]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// isCombinatorSegment reports whether a path segment is a combinator marker
// emitted by walkCombinators (e.g. "oneOf[0]") rather than a property name.
func isCombinatorSegment(seg string) bool {
	for _, combinator := range []string{"oneOf", "anyOf", "allOf"} {
		if strings.HasPrefix(seg, combinator+"[") && strings.HasSuffix(seg, "]") {
			return true
		}
	}
	return false
}

// renderHeaderValue converts a designated parameter's value to its header
// spelling, enforcing the schema's declared type and SEP-2243's integer range.
// A present empty string is sentinel-encoded so it remains distinguishable from
// an absent mirrored parameter; absent values are omitted by MirrorParamHeaders.
func renderHeaderValue(h ParamHeader, value any) (string, error) {
	where := strings.Join(h.Path, ".")
	switch h.Type {
	case "string":
		s, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("%w: parameter %q is declared string but got %T", ErrUnmirrorableValue, where, value)
		}
		if bad, invalid := firstControlChar(s); invalid {
			return "", fmt.Errorf(
				"%w: parameter %q contains a control character (%q)", ErrUnmirrorableValue, where, bad)
		}
		if s == "" {
			return EncodeSentinelName(s), nil
		}
		return s, nil
	case "boolean":
		b, ok := value.(bool)
		if !ok {
			return "", fmt.Errorf("%w: parameter %q is declared boolean but got %T", ErrUnmirrorableValue, where, value)
		}
		return strconv.FormatBool(b), nil
	case "integer":
		return renderIntegerHeaderValue(where, value)
	default:
		// Unreachable: ParamHeaders admits only the three types above.
		return "", fmt.Errorf("%w: parameter %q has unsupported type %q", ErrUnmirrorableValue, where, h.Type)
	}
}

// renderIntegerHeaderValue renders an integer-declared parameter. JSON decoding
// yields float64 for every number, so an integer arrives as a float that must be
// checked for integrality and for SEP-2243's safe-integer range; the int/int64
// cases cover arguments built in Go rather than decoded from JSON.
func renderIntegerHeaderValue(where string, value any) (string, error) {
	switch n := value.(type) {
	case float64:
		if n != math.Trunc(n) {
			return "", fmt.Errorf("%w: parameter %q is declared integer but got %v", ErrUnmirrorableValue, where, n)
		}
		if n > maxSafeInteger || n < -maxSafeInteger {
			return "", fmt.Errorf(
				"%w: parameter %q value %v is outside the safe integer range", ErrUnmirrorableValue, where, n)
		}
		return strconv.FormatInt(int64(n), 10), nil
	case int:
		return renderIntegerHeaderValue(where, float64(n))
	case int64:
		return renderIntegerHeaderValue(where, float64(n))
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return "", fmt.Errorf("%w: parameter %q is declared integer but got %q", ErrUnmirrorableValue, where, n)
		}
		return renderIntegerHeaderValue(where, float64(i))
	default:
		return "", fmt.Errorf(
			"%w: parameter %q is declared integer but got %T", ErrUnmirrorableValue, where, value)
	}
}

// firstControlChar returns the first control character in s, reporting true when
// one exists. CR and LF are the header-injection vectors; NUL and the other C0
// controls are equally illegal in a header value.
func firstControlChar(s string) (string, bool) {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return string(r), true
		}
	}
	return "", false
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
