// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive-core/mcpcompat/mcp"
)

func TestReadToolArgs(t *testing.T) {
	t.Parallel()

	t.Run("empty inputs yield nil", func(t *testing.T) {
		t.Parallel()
		args, err := readToolArgs("", "", strings.NewReader(""))
		require.NoError(t, err)
		assert.Nil(t, args)
	})

	t.Run("inline JSON object", func(t *testing.T) {
		t.Parallel()
		args, err := readToolArgs(`{"name":"world","count":3}`, "", strings.NewReader(""))
		require.NoError(t, err)
		assert.Equal(t, "world", args["name"])
		assert.InDelta(t, 3, args["count"], 0)
	})

	t.Run("stdin via dash", func(t *testing.T) {
		t.Parallel()
		args, err := readToolArgs("", "-", strings.NewReader(`{"foo":"bar"}`))
		require.NoError(t, err)
		assert.Equal(t, "bar", args["foo"])
	})

	t.Run("file path", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "args.json")
		require.NoError(t, os.WriteFile(path, []byte(`{"a":1}`), 0o600))
		args, err := readToolArgs("", path, strings.NewReader(""))
		require.NoError(t, err)
		assert.InDelta(t, 1, args["a"], 0)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		_, err := readToolArgs(`{not-json`, "", strings.NewReader(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parse tool arguments as JSON")
	})

	t.Run("non-object JSON is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := readToolArgs(`[1,2,3]`, "", strings.NewReader(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a JSON object")
	})

	t.Run("missing file returns error", func(t *testing.T) {
		t.Parallel()
		_, err := readToolArgs("", "/nonexistent/path/args.json", strings.NewReader(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read args file")
	})
}

func TestFormatBinaryContent(t *testing.T) {
	t.Parallel()

	t.Run("valid base64 reports decoded size", func(t *testing.T) {
		t.Parallel()
		// "hello" -> aGVsbG8= (5 bytes decoded)
		got := formatBinaryContent("image", "image/png", "aGVsbG8=")
		assert.Equal(t, "[image: image/png, 5 bytes]", got)
	})

	t.Run("invalid base64 falls back to encoded length", func(t *testing.T) {
		t.Parallel()
		got := formatBinaryContent("audio", "audio/wav", "!!!not-base64!!!")
		assert.Contains(t, got, "audio/wav")
		assert.Contains(t, got, "bytes]")
	})

	t.Run("empty mime type", func(t *testing.T) {
		t.Parallel()
		got := formatBinaryContent("image", "", "aGVsbG8=")
		assert.Contains(t, got, "unknown")
	})
}

func TestFormatContentResourceLink(t *testing.T) {
	t.Parallel()

	t.Run("full fields", func(t *testing.T) {
		t.Parallel()
		got := formatContent(mcp.ResourceLink{
			Type:     "resource_link",
			URI:      "file:///tmp/foo.txt",
			Name:     "foo.txt",
			MIMEType: "text/plain",
		})
		assert.Equal(t, "[resource link: foo.txt (file:///tmp/foo.txt, text/plain)]", got)
	})

	t.Run("missing name falls back to URI", func(t *testing.T) {
		t.Parallel()
		got := formatContent(mcp.ResourceLink{
			Type: "resource_link",
			URI:  "file:///tmp/foo.txt",
		})
		assert.Contains(t, got, "file:///tmp/foo.txt")
		assert.Contains(t, got, "unknown")
	})
}
