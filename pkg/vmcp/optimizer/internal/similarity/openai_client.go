// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package similarity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"time"
)

const (
	embeddingsPath = "/embeddings"

	// openAIMaxBatchSize is the OpenAI cap on inputs per /embeddings request;
	// compatible gateways generally honor the same limit.
	openAIMaxBatchSize = 2048
)

// openAIClient implements types.EmbeddingClient against an OpenAI-compatible
// /embeddings API (OpenAI, Azure OpenAI, or another OpenAI-compatible gateway).
type openAIClient struct {
	baseURL      string
	apiKey       string
	model        string
	headers      map[string]string
	httpClient   *http.Client
	maxBatchSize int
}

// newOpenAIClient creates a client that POSTs to baseURL+"/embeddings" using the
// given model. A non-empty apiKey is sent as a Bearer token; an empty apiKey
// omits the Authorization header so keyless endpoints work. headers are set on
// every request but cannot override Content-Type or Authorization. Zero timeout
// uses defaultTimeout.
func newOpenAIClient(baseURL, model, apiKey string, headers map[string]string, timeout time.Duration) (*openAIClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("OpenAI embedding base URL is required")
	}
	if model == "" {
		return nil, fmt.Errorf("OpenAI embedding model is required")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	if timeout == 0 {
		timeout = defaultTimeout
	}

	slog.Debug("OpenAI embedding client created",
		"base_url", baseURL, "model", model, "timeout", timeout, "custom_headers", len(headers))

	return &openAIClient{
		baseURL:      baseURL,
		apiKey:       apiKey,
		model:        model,
		headers:      maps.Clone(headers),
		httpClient:   &http.Client{Timeout: timeout},
		maxBatchSize: openAIMaxBatchSize,
	}, nil
}

type openAIEmbedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// EncodingFormat pins the response to float arrays, since we decode into
	// []float32; without it a compatible server may return base64.
	EncodingFormat string `json:"encoding_format"`
}

type openAIEmbedResponse struct {
	Data []openAIEmbedding `json:"data"`
}

type openAIEmbedding struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// Embed returns a vector embedding for the given text.
func (c *openAIClient) Embed(ctx context.Context, text string) ([]float32, error) {
	results, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("OpenAI returned empty response for single input")
	}
	return results[0], nil
}

// EmbedBatch returns embeddings for multiple texts, chunking to respect the
// OpenAI /embeddings input batch size.
func (c *openAIClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allEmbeddings := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += c.maxBatchSize {
		end := min(start+c.maxBatchSize, len(texts))
		embeddings, err := c.embedChunk(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	slog.Debug("OpenAI embedding batch completed",
		"inputs", len(texts), "chunks", (len(texts)+c.maxBatchSize-1)/c.maxBatchSize,
		"dimensions", len(allEmbeddings[0]))

	return allEmbeddings, nil
}

// embedChunk sends one batch to the /embeddings endpoint and returns the
// embeddings ordered to match texts.
func (c *openAIClient) embedChunk(ctx context.Context, texts []string) ([][]float32, error) {
	bodyBytes, err := json.Marshal(openAIEmbedRequest{Model: c.model, Input: texts, EncodingFormat: "float"})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OpenAI request: %w", err)
	}

	url := c.baseURL + embeddingsPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create OpenAI request: %w", err)
	}
	for name, value := range c.headers {
		req.Header.Set(name, value)
	}
	// Set after the custom headers so they can never be overridden.
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req) // #nosec G704 -- URL is built from the configured embedding base URL
	if err != nil {
		return nil, fmt.Errorf("OpenAI request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("OpenAI returned status %d: %s", resp.StatusCode, string(body))
	}

	var embedResp openAIEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&embedResp); err != nil {
		return nil, fmt.Errorf("failed to decode OpenAI response: %w", err)
	}

	if len(embedResp.Data) != len(texts) {
		return nil, fmt.Errorf("OpenAI returned %d embeddings for %d inputs", len(embedResp.Data), len(texts))
	}

	// Place each embedding at its reported index; the API is free to return
	// entries out of order.
	embeddings := make([][]float32, len(texts))
	for _, d := range embedResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("OpenAI returned out-of-range embedding index %d for %d inputs", d.Index, len(texts))
		}
		embeddings[d.Index] = d.Embedding
	}

	return embeddings, nil
}

// Close is a no-op for the OpenAI client.
func (*openAIClient) Close() error {
	return nil
}
