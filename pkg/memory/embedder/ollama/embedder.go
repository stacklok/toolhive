// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package ollama provides a memory.Embedder backed by a local Ollama server.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/stacklok/toolhive/pkg/memory"
)

// Embedder calls the Ollama /api/embeddings endpoint.
type Embedder struct {
	baseURL    string
	model      string
	dimensions int
	client     *http.Client
}

// New creates an Ollama embedder. It probes the server once to discover the
// embedding dimension. Returns an error if the server is unreachable or the
// model returns an empty vector.
func New(baseURL, model string) (*Embedder, error) {
	if _, err := url.ParseRequestURI(baseURL); err != nil {
		return nil, fmt.Errorf("invalid Ollama URL %q: %w", baseURL, err)
	}
	if model == "" {
		return nil, fmt.Errorf("model name is required")
	}
	e := &Embedder{baseURL: baseURL, model: model, client: &http.Client{}}

	emb, err := e.Embed(context.Background(), "probe")
	if err != nil {
		return nil, fmt.Errorf("probing Ollama embedder: %w", err)
	}
	e.dimensions = len(emb)
	return e, nil
}

// Embed calls the Ollama /api/embeddings endpoint and returns the vector.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(map[string]string{"model": e.model, "prompt": text})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/api/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling Ollama: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama returned status %d", resp.StatusCode)
	}

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding Ollama response: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama returned empty embedding")
	}
	return result.Embedding, nil
}

// Dimensions returns the fixed vector length produced by this embedder.
func (e *Embedder) Dimensions() int { return e.dimensions }

var _ memory.Embedder = (*Embedder)(nil)
