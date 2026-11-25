// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/mcp"
)

// The argument hash is computed by the client that asks for a call-bound
// token and by the vMCP that checks it, so both sides test against one set of
// vectors. testdata/argumenthash/vectors.json is the canonical copy; the
// client's test compares its copy with this one.
//
// The file started as a verbatim copy of Mecatl's
// internal/adapter/mcpbroker/testdata/argumenthash/vectors.json at commit
// 4c8816d1ba9fc804fa45336593cb25dd9a07d1fb, "feat(mcpbroker): authorize
// gateway calls with the identity broker and send them with a per-call token"
// (stacklok/mecatl#2818), unchanged on Mecatl main as of
// e9226ba8c9a078713ff7a2424a7ed65043d4fee6. Since then it differs from that
// copy: it marks vectors with gateway_refuses, and it holds the refusal cases
// that were inline in Mecatl's argumenthash_test.go at the same commit. Mecatl
// switches to this copy in "Bind each gateway token to its call"
// (stacklok/mecatl#2727).
//
// Expected canonical strings come from RFC 8785 and hand-checked cases; sha256
// is computed over the expected string, not by this package.

type hashVector struct {
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments"`
	Canonical      string          `json:"canonical"`
	SHA256         string          `json:"sha256"`
	GatewayRefuses bool            `json:"gateway_refuses"`
}

type hashRefusal struct {
	Name   string  `json:"name"`
	Text   *string `json:"arguments_text"`
	Base64 *string `json:"arguments_base64"`
}

type vectorsFile struct {
	Vectors  []hashVector  `json:"vectors"`
	Refusals []hashRefusal `json:"refusals"`
}

func loadVectors(t *testing.T) vectorsFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "argumenthash", "vectors.json"))
	require.NoError(t, err)
	var file vectorsFile
	require.NoError(t, json.Unmarshal(raw, &file))
	require.NotEmpty(t, file.Vectors)
	require.NotEmpty(t, file.Refusals)
	return file
}

// TestArgumentsSHA256Vectors runs every vector, including those marked
// gateway_refuses: the hash must agree with the other side on them too.
func TestArgumentsSHA256Vectors(t *testing.T) {
	t.Parallel()
	for _, v := range loadVectors(t).Vectors {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			sum := sha256.Sum256([]byte(v.Canonical))
			require.Equal(t, v.SHA256, hex.EncodeToString(sum[:]), "vector's sha256 is not the hash of its canonical string")

			before := bytes.Clone(v.Arguments)
			canonical, err := canonicalArguments(v.Arguments)
			require.NoError(t, err)
			assert.Equal(t, v.Canonical, string(canonical))

			got, err := ArgumentsSHA256(v.Arguments)
			require.NoError(t, err)
			assert.Equal(t, v.SHA256, got)
			assert.Equal(t, before, []byte(v.Arguments), "arguments were modified")
		})
	}
}

// TestVectorsGatewayRefuses keeps each vector's gateway_refuses flag true to
// the vMCP: the flag must be set exactly when ToolHive's message decoder
// refuses a tools/call carrying the vector's arguments with -32600 Invalid
// Request before the hash runs. The decoder refuses member names that collide
// after case folding, at any depth (pkg/mcp/json_ambiguity.go), so a client
// that sends such arguments gets a request error, not a hash mismatch. This
// checks the shared file against the decoder rather than restating its rule,
// so a change to either fails here.
func TestVectorsGatewayRefuses(t *testing.T) {
	t.Parallel()
	vectors := loadVectors(t).Vectors
	flagged := map[string]bool{}
	for _, v := range vectors {
		if v.GatewayRefuses {
			flagged[v.Name] = true
		}
	}
	// "ASCII key order" has both "A" and "a"; pin it so the flag cannot be
	// dropped from the file together with a decoder change.
	require.True(t, flagged["ASCII key order"], `"ASCII key order" must be marked gateway_refuses`)

	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			params := `{"name":"t"}`
			if v.Arguments != nil {
				params = `{"name":"t","arguments":` + string(v.Arguments) + `}`
			}
			body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}`)

			_, err := mcp.DecodeMessage(body)
			if !v.GatewayRefuses {
				require.NoError(t, err)
				return
			}
			var coded mcp.CodedError
			require.ErrorAs(t, err, &coded)
			assert.Equal(t, mcp.CodeInvalidRequest, coded.Code())
		})
	}
}

func TestArgumentsSHA256Refusals(t *testing.T) {
	t.Parallel()
	for _, r := range loadVectors(t).Refusals {
		t.Run(r.Name, func(t *testing.T) {
			t.Parallel()
			require.True(t, (r.Text == nil) != (r.Base64 == nil), "want exactly one of arguments_text and arguments_base64")
			args := func() []byte {
				if r.Text != nil {
					return []byte(*r.Text)
				}
				b, err := base64.StdEncoding.DecodeString(*r.Base64)
				require.NoError(t, err)
				require.False(t, utf8.Valid(b), "arguments_base64 is for bytes that are not valid UTF-8; use arguments_text")
				return b
			}()

			got, err := ArgumentsSHA256(args)
			require.ErrorIs(t, err, ErrNotIJSON)
			assert.Empty(t, got)
			assert.NotContains(t, err.Error(), `"`, "the error must not quote the arguments")
		})
	}
}

// TestArgumentsSHA256SurroundingWhitespace covers forms the shared vectors
// cannot express, because a raw JSON member in a file never carries the
// whitespace around it: a value that is only JSON whitespace, and null or an
// object surrounded by it. Absent arguments are the vector "absent arguments".
func TestArgumentsSHA256SurroundingWhitespace(t *testing.T) {
	t.Parallel()
	const emptyObjectSHA256 = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
	for name, args := range map[string]json.RawMessage{
		"JSON whitespace":      json.RawMessage(" \t\r\n"),
		"null in whitespace":   json.RawMessage("\n null \t"),
		"object in whitespace": json.RawMessage("\r\n {\t} \n"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := ArgumentsSHA256(args)
			require.NoError(t, err)
			assert.Equal(t, emptyObjectSHA256, got)
		})
	}
}

func TestIsExactly2To53(t *testing.T) {
	t.Parallel()
	for lit, want := range map[string]bool{
		"9007199254740992":        true,
		"-9007199254740992":       true,
		"9007199254740992.000":    true,
		"9.007199254740992e15":    true,
		"9.007199254740992E+15":   true,
		"90071992547409920e-1":    true,
		"0.9007199254740992e16":   true,
		"900719925474099.2e1":     true,
		"9007199254740992e0":      true,
		"9007199254740993":        false,
		"9007199254740992.5":      false,
		"9007199254740991.9":      false,
		"9.0071992547409921e15":   false,
		"9007199254740992e1":      false,
		"900719925474099.2":       false,
		"9007199254740992e999999": false,
		"9007199254740992e":       false,
	} {
		t.Run(lit, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, want, isExactly2To53(lit))
		})
	}
}
