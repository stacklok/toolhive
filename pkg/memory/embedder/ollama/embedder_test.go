// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ollama_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/memory/embedder/ollama"
)

func TestEmbed(t *testing.T) {
	t.Parallel()

	want := []float32{0.1, 0.2, 0.3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/embeddings", r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"embedding": want})
	}))
	t.Cleanup(srv.Close)

	e, err := ollama.New(srv.URL, "nomic-embed-text")
	require.NoError(t, err)
	require.Equal(t, 3, e.Dimensions())

	got, err := e.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	require.InDeltaSlice(t, want, got, 0.001)
}
