// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func Test_newOpenAIClient(t *testing.T) {
	t.Parallel()

	t.Run("empty URL returns error", func(t *testing.T) {
		t.Parallel()
		client, err := newOpenAIClient("", "text-embedding-3-small", "key", nil, 0)
		require.ErrorContains(t, err, "OpenAI embedding base URL is required")
		require.Nil(t, client)
	})

	t.Run("empty model returns error", func(t *testing.T) {
		t.Parallel()
		client, err := newOpenAIClient("http://embeddings:8080/v1", "", "key", nil, 0)
		require.ErrorContains(t, err, "OpenAI embedding model is required")
		require.Nil(t, client)
	})

	t.Run("valid args create client with default batch size", func(t *testing.T) {
		t.Parallel()
		client, err := newOpenAIClient("http://embeddings:8080/v1", "text-embedding-3-small", "key", nil, 0)
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, openAIMaxBatchSize, client.maxBatchSize)
		require.Equal(t, defaultTimeout, client.httpClient.Timeout)
	})

	t.Run("custom timeout", func(t *testing.T) {
		t.Parallel()
		client, err := newOpenAIClient("http://embeddings:8080/v1", "text-embedding-3-small", "key", nil, 5*time.Second)
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, 5*time.Second, client.httpClient.Timeout)
	})

	t.Run("headers are cloned at construction", func(t *testing.T) {
		t.Parallel()
		headers := map[string]string{"x-cache-key": "toolhive"}
		client, err := newOpenAIClient("http://embeddings:8080/v1", "text-embedding-3-small", "key", headers, 0)
		require.NoError(t, err)
		headers["x-cache-key"] = "mutated"
		require.Equal(t, "toolhive", client.headers["x-cache-key"])
	})
}

func TestOpenAIClient_Embed(t *testing.T) {
	t.Parallel()

	expected := []float32{0.1, 0.2, 0.3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, embeddingsPath, r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var req openAIEmbedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Equal(t, "text-embedding-3-small", req.Model)
		require.Equal(t, "float", req.EncodingFormat)
		require.Len(t, req.Input, 1)
		require.Equal(t, "hello world", req.Input[0])

		writeOpenAIEmbeddings(t, w, [][]float32{expected})
	}))
	t.Cleanup(srv.Close)

	client := newTestOpenAIClient(t, srv.URL, "test-key")

	result, err := client.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	require.Equal(t, expected, result)
}

func TestOpenAIClient_EmbedBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		texts      []string
		handler    http.HandlerFunc
		wantErr    string
		wantLen    int
		wantResult [][]float32
	}{
		{
			name:  "empty input",
			texts: nil,
		},
		{
			name:  "single input",
			texts: []string{"hello"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeOpenAIEmbeddings(t, w, [][]float32{{0.1, 0.2}})
			},
			wantLen:    1,
			wantResult: [][]float32{{0.1, 0.2}},
		},
		{
			name:  "multiple inputs",
			texts: []string{"hello", "world"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeOpenAIEmbeddings(t, w, [][]float32{{0.1, 0.2}, {0.3, 0.4}})
			},
			wantLen:    2,
			wantResult: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		},
		{
			name:  "out-of-order data is reordered by index",
			texts: []string{"hello", "world"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(openAIEmbedResponse{Data: []openAIEmbedding{
					{Index: 1, Embedding: []float32{0.3, 0.4}},
					{Index: 0, Embedding: []float32{0.1, 0.2}},
				}})
			},
			wantLen:    2,
			wantResult: [][]float32{{0.1, 0.2}, {0.3, 0.4}},
		},
		{
			name:  "server error",
			texts: []string{"hello"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("internal error"))
			},
			wantErr: "OpenAI returned status 500",
		},
		{
			name:  "mismatched count",
			texts: []string{"hello", "world"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeOpenAIEmbeddings(t, w, [][]float32{{0.1, 0.2}})
			},
			wantErr: "OpenAI returned 1 embeddings for 2 inputs",
		},
		{
			name:  "out-of-range index",
			texts: []string{"hello"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(openAIEmbedResponse{Data: []openAIEmbedding{
					{Index: 5, Embedding: []float32{0.1, 0.2}},
				}})
			},
			wantErr: "out-of-range embedding index 5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var srv *httptest.Server
			if tt.handler != nil {
				srv = httptest.NewServer(tt.handler)
				t.Cleanup(srv.Close)
			}

			baseURL := "http://localhost:0"
			if srv != nil {
				baseURL = srv.URL
			}

			client := newTestOpenAIClient(t, baseURL, "test-key")

			results, err := client.EmbedBatch(context.Background(), tt.texts)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			if tt.wantLen > 0 {
				require.Len(t, results, tt.wantLen)
				require.Equal(t, tt.wantResult, results)
			} else {
				require.Nil(t, results)
			}
		})
	}
}

func TestOpenAIClient_EmbedBatch_Chunking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxBatchSize int
		numInputs    int
		wantChunks   int
	}{
		{name: "inputs fit in single batch", maxBatchSize: 5, numInputs: 3, wantChunks: 1},
		{name: "inputs exactly fill one batch", maxBatchSize: 4, numInputs: 4, wantChunks: 1},
		{name: "inputs split into two batches", maxBatchSize: 3, numInputs: 5, wantChunks: 2},
		{name: "inputs split into many batches", maxBatchSize: 2, numInputs: 7, wantChunks: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var chunkCount int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req openAIEmbedRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.LessOrEqual(t, len(req.Input), tt.maxBatchSize,
					"chunk size should not exceed maxBatchSize")
				chunkCount++

				embeddings := make([][]float32, len(req.Input))
				for i := range embeddings {
					embeddings[i] = []float32{float32(i) * 0.1}
				}
				writeOpenAIEmbeddings(t, w, embeddings)
			}))
			t.Cleanup(srv.Close)

			texts := make([]string, tt.numInputs)
			for i := range texts {
				texts[i] = fmt.Sprintf("text-%d", i)
			}

			client := newTestOpenAIClientWithBatch(t, srv.URL, tt.maxBatchSize)
			results, err := client.EmbedBatch(context.Background(), texts)
			require.NoError(t, err)
			require.Len(t, results, tt.numInputs)
			require.Equal(t, tt.wantChunks, chunkCount)
		})
	}
}

func TestOpenAIClient_EmbedBatch_ChunkErrorStopsEarly(t *testing.T) {
	t.Parallel()

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server overloaded"))
			return
		}
		writeOpenAIEmbeddings(t, w, [][]float32{{0.1}, {0.2}})
	}))
	t.Cleanup(srv.Close)

	texts := make([]string, 6) // 3 chunks of 2
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	client := newTestOpenAIClientWithBatch(t, srv.URL, 2)
	_, err := client.EmbedBatch(context.Background(), texts)
	require.ErrorContains(t, err, "OpenAI returned status 500")
	require.Equal(t, 2, callCount, "should stop after the failing chunk")
}

func TestOpenAIClient_OmitsAuthHeaderWhenKeyless(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Empty(t, r.Header.Get("Authorization"))
		writeOpenAIEmbeddings(t, w, [][]float32{{0.1}})
	}))
	t.Cleanup(srv.Close)

	client := newTestOpenAIClient(t, srv.URL, "")

	_, err := client.Embed(context.Background(), "hello")
	require.NoError(t, err)
}

func TestOpenAIClient_SendsCustomHeaders(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "toolhive-optimizer", r.Header.Get("x-cache-key"))
		require.Equal(t, "eu-west", r.Header.Get("X-Gateway-Region"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		writeOpenAIEmbeddings(t, w, [][]float32{{0.1}})
	}))
	t.Cleanup(srv.Close)

	client, err := newOpenAIClient(srv.URL, "text-embedding-3-small", "test-key", map[string]string{
		"x-cache-key":      "toolhive-optimizer",
		"X-Gateway-Region": "eu-west",
	}, 0)
	require.NoError(t, err)

	_, err = client.Embed(context.Background(), "hello")
	require.NoError(t, err)
}

func TestOpenAIClient_ProtocolHeadersWinOverCustomHeaders(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		writeOpenAIEmbeddings(t, w, [][]float32{{0.1}})
	}))
	t.Cleanup(srv.Close)

	client, err := newOpenAIClient(srv.URL, "text-embedding-3-small", "test-key", map[string]string{
		"authorization": "Bearer spoofed",
		"content-type":  "text/plain",
	}, 0)
	require.NoError(t, err)

	_, err = client.Embed(context.Background(), "hello")
	require.NoError(t, err)
}

func TestOpenAIClient_Close(t *testing.T) {
	t.Parallel()

	client := newTestOpenAIClient(t, "http://my-embedding:8080/v1", "key")
	require.NoError(t, client.Close())
}

// writeOpenAIEmbeddings encodes embeddings as an OpenAI /embeddings response,
// assigning each entry its slice position as the index.
func writeOpenAIEmbeddings(t *testing.T, w http.ResponseWriter, embeddings [][]float32) {
	t.Helper()
	resp := openAIEmbedResponse{Data: make([]openAIEmbedding, len(embeddings))}
	for i, e := range embeddings {
		resp.Data[i] = openAIEmbedding{Index: i, Embedding: e}
	}
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(resp))
}

// newTestOpenAIClient creates an openAIClient pointing at the given URL for
// testing. It defaults to a large batch size so requests are single-chunk.
func newTestOpenAIClient(t *testing.T, baseURL, apiKey string) *openAIClient {
	t.Helper()
	client := newTestOpenAIClientWithBatch(t, baseURL, 1000)
	client.apiKey = apiKey
	return client
}

// newTestOpenAIClientWithBatch creates an openAIClient with a specific max batch
// size for testing, using a fixed API key.
func newTestOpenAIClientWithBatch(t *testing.T, baseURL string, maxBatchSize int) *openAIClient {
	t.Helper()
	return &openAIClient{
		baseURL:      baseURL,
		apiKey:       "test-key",
		model:        "text-embedding-3-small",
		httpClient:   &http.Client{Timeout: defaultTimeout},
		maxBatchSize: maxBatchSize,
	}
}
