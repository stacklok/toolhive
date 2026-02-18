// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	vmcpconfig "github.com/stacklok/toolhive/pkg/vmcp/config"
)

func TestNewEmbeddingClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     *vmcpconfig.OptimizerConfig
		wantNil bool
		wantErr string
	}{
		{
			name:    "nil config returns nil",
			cfg:     nil,
			wantNil: true,
		},
		{
			name:    "empty embedding service returns nil",
			cfg:     &vmcpconfig.OptimizerConfig{},
			wantNil: true,
		},
		{
			name: "valid URL creates client",
			cfg:  &vmcpconfig.OptimizerConfig{EmbeddingService: "http://my-embedding:8080"},
		},
		{
			name: "URL with namespace",
			cfg:  &vmcpconfig.OptimizerConfig{EmbeddingService: "http://my-embedding.ml.svc.cluster.local:8080"},
		},
		{
			name: "custom timeout",
			cfg: &vmcpconfig.OptimizerConfig{
				EmbeddingService:        "http://my-embedding:8080",
				EmbeddingServiceTimeout: vmcpconfig.Duration(5 * time.Second),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			client, err := NewEmbeddingClient(tt.cfg)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, client)
			} else {
				require.NoError(t, err)
				if tt.wantNil {
					require.Nil(t, client)
				} else {
					require.NotNil(t, client)
				}
			}
		})
	}
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

func TestTEIClient_Close(t *testing.T) {
	t.Parallel()

	client, err := newTEIClient("http://my-embedding:8080", 0)
	require.NoError(t, err)
	require.NoError(t, client.Close())
}

// newTestTEIClient creates a teiClient pointing at the given URL for testing.
// This bypasses newTEIClient since test servers have dynamic URLs that don't
// map to a Kubernetes service name.
func newTestTEIClient(t *testing.T, baseURL string) *teiClient {
	t.Helper()
	return &teiClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
}
