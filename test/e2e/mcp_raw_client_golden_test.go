// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// goldenModernRequestFixture is a Modern (2026-07-28) "tools/list" request
// emitted by a real github.com/modelcontextprotocol/go-sdk v1.7.0-pre.3 client.
// It exists so NewModernRequest's _meta shape is checked against an independent
// oracle, not just against assertions written by the same author who wrote the
// encoder. To regenerate: point any go-sdk v1.7 client at
// `yardstick-server --stateless` (>= v1.2.0, itself on go-sdk v1.7.0-pre.3) and
// capture the raw tools/list request body it sends.
const goldenModernRequestFixture = "testdata/golden_modern_request.json"

// extractMeta pulls params._meta out of a decoded wire object as a
// map[string]any, failing the test with a clear message if the shape isn't
// what's expected.
func extractMeta(t *testing.T, wire map[string]any) map[string]any {
	t.Helper()
	params, ok := wire["params"].(map[string]any)
	require.True(t, ok, "wire request must have an object params field: %#v", wire["params"])
	meta, ok := params["_meta"].(map[string]any)
	require.True(t, ok, "params must have an object _meta field: %#v", params["_meta"])
	return meta
}

// jsonKind reports the JSON type of a value decoded by encoding/json into
// an any (object/array/string/number/bool/null), discarding its content.
func jsonKind(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v) // unreachable for encoding/json output
	}
}

// metaShape reduces a decoded _meta map to its keys and each value's JSON
// kind, discarding nested content. NewModernRequest deliberately sends a
// minimal empty clientCapabilities object rather than mirroring the go-sdk
// client's specific declared capabilities (roots, sampling, ...), so nested
// capability fields are never expected to match value-for-value — only that
// the same top-level _meta keys exist and each is the same kind of JSON
// value ("shape/keys", per the acceptance criteria).
func metaShape(meta map[string]any) map[string]string {
	shape := make(map[string]string, len(meta))
	for k, v := range meta {
		shape[k] = jsonKind(v)
	}
	return shape
}

// TestModernRequestMatchesGoldenSDKFixture checks that NewModernRequest's
// wire output byte-matches the golden go-sdk fixture's _meta shape and keys.
//
// A literal full-envelope byte comparison would not hold even for two
// semantically identical requests: RawRequest.marshal builds the body from a
// map[string]any (encoding/json sorts map keys alphabetically), while the
// go-sdk marshals a Go struct in its own field order. So this test scopes
// the byte-level comparison to what the two sides can actually agree on: the
// _meta object's keys and each value's kind, independently re-marshaled on
// each side. Since encoding/json deterministically sorts map keys, comparing
// the re-marshaled bytes of two map[string]string shapes is still a genuine
// byte-for-byte check — just correctly scoped to the part of the AC that is
// comparable byte-for-byte ("_meta shape/keys", not deep capability content
// that NewModernRequest never claims to reproduce).
func TestModernRequestMatchesGoldenSDKFixture(t *testing.T) {
	t.Parallel()

	goldenBytes, err := os.ReadFile(goldenModernRequestFixture)
	require.NoError(t, err, "golden fixture must exist; see the goldenModernRequestFixture doc comment to regenerate it")
	var golden map[string]any
	require.NoError(t, json.Unmarshal(goldenBytes, &golden))
	require.Equal(t, "tools/list", golden["method"], "golden fixture must have been captured for tools/list to match NewModernRequest below")
	goldenMeta := extractMeta(t, golden)

	req, err := NewModernRequest("tools/list", nil)
	require.NoError(t, err)
	rawMeta := extractMeta(t, wireOf(t, req))

	require.Equal(t, goldenMeta[MetaKeyProtocolVersion], rawMeta[MetaKeyProtocolVersion],
		"protocolVersion must match exactly, not just in shape")

	// clientInfo is present in the SDK-captured fixture (the SDK client has an
	// Implementation configured) but omitted by NewModernRequest per spec
	// ("SHOULD", not "MUST" — see NewModernRequest's doc comment); exclude it
	// from the shape comparison below rather than requiring both sides to
	// agree on an optional key.
	require.Contains(t, goldenMeta, MetaKeyClientInfo, "golden fixture is expected to carry optional clientInfo")
	require.NotContains(t, rawMeta, MetaKeyClientInfo, "NewModernRequest omits clientInfo by default")
	delete(goldenMeta, MetaKeyClientInfo)

	goldenShapeJSON, err := json.Marshal(metaShape(goldenMeta))
	require.NoError(t, err)
	rawShapeJSON, err := json.Marshal(metaShape(rawMeta))
	require.NoError(t, err)

	require.True(t, bytes.Equal(goldenShapeJSON, rawShapeJSON),
		"raw client _meta must byte-match the golden go-sdk fixture's _meta shape/keys (excluding optional clientInfo)\ngolden shape: %s\nraw shape:    %s",
		goldenShapeJSON, rawShapeJSON)
}
