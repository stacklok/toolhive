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

func Test_newTEIClient(t *testing.T) {
	t.Parallel()

	infoHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == infoPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"max_client_batch_size": 16}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})

	t.Run("empty URL returns error", func(t *testing.T) {
		t.Parallel()
		client, err := newTEIClient("", 0)
		require.ErrorContains(t, err, "TEI BaseURL is required")
		require.Nil(t, client)
	})

	t.Run("valid URL creates client with batch size from info", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(infoHandler)
		defer srv.Close()

		client, err := newTEIClient(srv.URL, 0)
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, 16, client.maxBatchSize)
	})

	t.Run("custom timeout", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(infoHandler)
		defer srv.Close()

		client, err := newTEIClient(srv.URL, 5*time.Second)
		require.NoError(t, err)
		require.NotNil(t, client)
	})

	t.Run("unreachable info endpoint uses default batch size", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		client, err := newTEIClient(srv.URL, 0)
		require.NoError(t, err)
		require.NotNil(t, client)
		require.Equal(t, defaultMaxBatchSize, client.maxBatchSize)
	})
}

func TestTEIClient_Embed(t *testing.T) {
	t.Parallel()

	expected := []float32{0.1, 0.2, 0.3}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, embedPath, r.URL.Path)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		var req embedRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Len(t, req.Inputs, 1)
		require.Equal(t, "hello world", req.Inputs[0])
		require.True(t, req.Truncate)

		w.Header().Set("Content-Type", "application/json")
		// TEI returns [][]float32
		require.NoError(t, json.NewEncoder(w).Encode([][]float32{expected}))
	}))
	defer srv.Close()

	client := newTestTEIClient(t, srv.URL)

	result, err := client.Embed(context.Background(), "hello world")
	require.NoError(t, err)
	require.Equal(t, expected, result)
}

func TestTEIClient_EmbedBatch(t *testing.T) {
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
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([][]float32{{0.1, 0.2}})
			},
			wantLen:    1,
			wantResult: [][]float32{{0.1, 0.2}},
		},
		{
			name:  "multiple inputs",
			texts: []string{"hello", "world"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([][]float32{{0.1, 0.2}, {0.3, 0.4}})
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
			wantErr: "TEI returned status 500",
		},
		{
			name:  "mismatched count",
			texts: []string{"hello", "world"},
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode([][]float32{{0.1, 0.2}})
			},
			wantErr: "TEI returned 1 embeddings for 2 inputs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var srv *httptest.Server
			if tt.handler != nil {
				srv = httptest.NewServer(tt.handler)
				defer srv.Close()
			}

			baseURL := "http://localhost:0"
			if srv != nil {
				baseURL = srv.URL
			}

			client := newTestTEIClient(t, baseURL)

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

func TestTEIClient_EmbedBatch_Chunking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxBatchSize int
		numInputs    int
		wantChunks   int
	}{
		{
			name:         "inputs fit in single batch",
			maxBatchSize: 5,
			numInputs:    3,
			wantChunks:   1,
		},
		{
			name:         "inputs exactly fill one batch",
			maxBatchSize: 4,
			numInputs:    4,
			wantChunks:   1,
		},
		{
			name:         "inputs split into two batches",
			maxBatchSize: 3,
			numInputs:    5,
			wantChunks:   2,
		},
		{
			name:         "inputs split into many batches",
			maxBatchSize: 2,
			numInputs:    7,
			wantChunks:   4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var chunkCount int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req embedRequest
				require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
				require.LessOrEqual(t, len(req.Inputs), tt.maxBatchSize,
					"chunk size should not exceed maxBatchSize")
				chunkCount++

				embeddings := make([][]float32, len(req.Inputs))
				for i := range embeddings {
					embeddings[i] = []float32{float32(i) * 0.1}
				}
				w.Header().Set("Content-Type", "application/json")
				require.NoError(t, json.NewEncoder(w).Encode(embeddings))
			}))
			defer srv.Close()

			texts := make([]string, tt.numInputs)
			for i := range texts {
				texts[i] = fmt.Sprintf("text-%d", i)
			}

			client := newTestTEIClientWithBatch(t, srv.URL, tt.maxBatchSize)
			results, err := client.EmbedBatch(context.Background(), texts)
			require.NoError(t, err)
			require.Len(t, results, tt.numInputs)
			require.Equal(t, tt.wantChunks, chunkCount)
		})
	}
}

func TestTEIClient_EmbedBatch_ChunkErrorStopsEarly(t *testing.T) {
	t.Parallel()

	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount++
		if callCount == 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("server overloaded"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([][]float32{{0.1}, {0.2}})
	}))
	defer srv.Close()

	texts := make([]string, 6) // 3 chunks of 2
	for i := range texts {
		texts[i] = fmt.Sprintf("text-%d", i)
	}

	client := newTestTEIClientWithBatch(t, srv.URL, 2)
	_, err := client.EmbedBatch(context.Background(), texts)
	require.ErrorContains(t, err, "TEI returned status 500")
	require.Equal(t, 2, callCount, "should stop after the failing chunk")
}

func Test_fetchMaxBatchSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		wantSize int
		wantErr  string
	}{
		{
			name: "returns reported batch size",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"max_client_batch_size": 64, "model_type": "bert"}`))
			},
			wantSize: 64,
		},
		{
			name: "zero batch size returns default",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"max_client_batch_size": 0}`))
			},
			wantSize: defaultMaxBatchSize,
		},
		{
			name: "missing field returns default",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model_type": "bert"}`))
			},
			wantSize: defaultMaxBatchSize,
		},
		{
			name: "server error returns error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantErr: "TEI /info returned status 500",
		},
		{
			name: "invalid JSON returns error",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`not json`))
			},
			wantErr: "failed to decode TEI /info response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			size, err := fetchMaxBatchSize(srv.URL, srv.Client())
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantSize, size)
		})
	}
}

func Test_fetchMaxBatchSize_ConnectionRefused(t *testing.T) {
	t.Parallel()

	_, err := fetchMaxBatchSize("http://localhost:1", &http.Client{Timeout: time.Second})
	require.Error(t, err)
	require.ErrorContains(t, err, "TEI /info request failed")
}

func TestTEIClient_Close(t *testing.T) {
	t.Parallel()

	client := newTestTEIClient(t, "http://my-embedding:8080")
	require.NoError(t, client.Close())
}

// newTestTEIClient creates a teiClient pointing at the given URL for testing.
// This bypasses newTEIClient since test servers have dynamic URLs that don't
// map to a Kubernetes service name. It defaults to a large batch size so
// existing tests behave as single-chunk requests.
func newTestTEIClient(t *testing.T, baseURL string) *teiClient {
	t.Helper()
	return newTestTEIClientWithBatch(t, baseURL, 1000)
}

// newTestTEIClientWithBatch creates a teiClient with a specific max batch size for testing.
func newTestTEIClientWithBatch(t *testing.T, baseURL string, maxBatchSize int) *teiClient {
	t.Helper()
	return &teiClient{
		baseURL:      baseURL,
		httpClient:   &http.Client{Timeout: defaultTimeout},
		maxBatchSize: maxBatchSize,
	}
}
