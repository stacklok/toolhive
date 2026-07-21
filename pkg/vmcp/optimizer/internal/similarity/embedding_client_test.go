// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

func TestNewEmbeddingClient(t *testing.T) {
	t.Parallel()

	// TEI selection queries the /info endpoint on construction, so a stub server
	// is needed for that case.
	teiInfo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == infoPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"max_client_batch_size": 16}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(teiInfo.Close)

	t.Run("nil config disables semantic search", func(t *testing.T) {
		t.Parallel()
		client, err := NewEmbeddingClient(nil)
		require.NoError(t, err)
		require.Nil(t, client)
	})

	t.Run("empty service disables semantic search", func(t *testing.T) {
		t.Parallel()
		client, err := NewEmbeddingClient(&types.OptimizerConfig{EmbeddingProvider: types.EmbeddingProviderOpenAI})
		require.NoError(t, err)
		require.Nil(t, client)
	})

	t.Run("empty provider defaults to TEI", func(t *testing.T) {
		t.Parallel()
		client, err := NewEmbeddingClient(&types.OptimizerConfig{EmbeddingService: teiInfo.URL})
		require.NoError(t, err)
		require.IsType(t, &teiClient{}, client)
	})

	t.Run("tei provider", func(t *testing.T) {
		t.Parallel()
		client, err := NewEmbeddingClient(&types.OptimizerConfig{
			EmbeddingService:  teiInfo.URL,
			EmbeddingProvider: types.EmbeddingProviderTEI,
		})
		require.NoError(t, err)
		require.IsType(t, &teiClient{}, client)
	})

	t.Run("openai provider", func(t *testing.T) {
		t.Parallel()
		client, err := NewEmbeddingClient(&types.OptimizerConfig{
			EmbeddingService:  "http://embeddings:8080/v1",
			EmbeddingProvider: types.EmbeddingProviderOpenAI,
			EmbeddingModel:    "text-embedding-3-small",
		})
		require.NoError(t, err)
		require.IsType(t, &openAIClient{}, client)
	})

	t.Run("unsupported provider returns error", func(t *testing.T) {
		t.Parallel()
		client, err := NewEmbeddingClient(&types.OptimizerConfig{
			EmbeddingService:  "http://embeddings:8080",
			EmbeddingProvider: "cohere",
		})
		require.ErrorContains(t, err, "unsupported embedding provider")
		require.Nil(t, client)
	})
}
