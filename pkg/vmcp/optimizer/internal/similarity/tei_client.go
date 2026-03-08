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
	"net/http"
	"time"

	"github.com/stacklok/toolhive/pkg/vmcp/optimizer/internal/types"
)

const (
	// defaultTimeout is the default HTTP timeout for TEI requests.
	defaultTimeout = 30 * time.Second

	// embedPath is the TEI endpoint path for generating embeddings.
	embedPath = "/embed"

	// infoPath is the TEI endpoint that returns server metadata including max batch size.
	infoPath = "/info"

	// defaultMaxBatchSize is used when the TEI /info endpoint does not report a max batch size.
	defaultMaxBatchSize = 32
)

// teiClient implements types.EmbeddingClient by calling the HuggingFace
// Text Embeddings Inference (TEI) HTTP API.
type teiClient struct {
	baseURL      string
	httpClient   *http.Client
	maxBatchSize int
}

// NewEmbeddingClient creates an EmbeddingClient from the given optimizer
// configuration. It returns (nil, nil) if cfg is nil or no embedding service
// URL is configured, meaning semantic search will be disabled.
func NewEmbeddingClient(cfg *types.OptimizerConfig) (types.EmbeddingClient, error) {
	if cfg == nil || cfg.EmbeddingService == "" {
		return nil, nil
	}
	return newTEIClient(cfg.EmbeddingService, cfg.EmbeddingServiceTimeout)
}

// newTEIClient creates a new TEI embedding client that calls the specified endpoint.
// It queries the TEI /info endpoint to discover the server's maximum batch size.
func newTEIClient(baseURL string, timeout time.Duration) (*teiClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("TEI BaseURL is required")
	}

	if timeout == 0 {
		timeout = defaultTimeout
	}

	httpClient := &http.Client{Timeout: timeout}

	maxBatch, err := fetchMaxBatchSize(baseURL, httpClient)
	if err != nil {
		slog.Warn("failed to query TEI /info, using default max batch size",
			"error", err, "default", defaultMaxBatchSize)
		maxBatch = defaultMaxBatchSize
	}

	slog.Debug("TEI embedding client created",
		"base_url", baseURL, "timeout", timeout, "max_batch_size", maxBatch)

	return &teiClient{
		baseURL:      baseURL,
		httpClient:   httpClient,
		maxBatchSize: maxBatch,
	}, nil
}

// teiInfoResponse is a subset of the TEI /info endpoint response.
type teiInfoResponse struct {
	MaxClientBatchSize int `json:"max_client_batch_size"`
}

// fetchMaxBatchSize queries the TEI /info endpoint and returns the max client batch size.
func fetchMaxBatchSize(baseURL string, httpClient *http.Client) (int, error) {
	resp, err := httpClient.Get(baseURL + infoPath) // #nosec G107 -- URL is built from the configured TEI base URL
	if err != nil {
		return 0, fmt.Errorf("TEI /info request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("TEI /info returned status %d", resp.StatusCode)
	}

	var info teiInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return 0, fmt.Errorf("failed to decode TEI /info response: %w", err)
	}

	if info.MaxClientBatchSize <= 0 {
		return defaultMaxBatchSize, nil
	}

	return info.MaxClientBatchSize, nil
}

// embedRequest is the JSON body sent to the TEI /embed endpoint.
type embedRequest struct {
	Inputs []string `json:"inputs"`
	// Truncate tells the TEI server to silently truncate input texts that
	// exceed the model's maximum token length instead of returning an error.
	// We always set this to true because tool descriptions may exceed model
	// limits and we prefer embedding a truncated description over a request failure.
	Truncate bool `json:"truncate"`
}

// Embed returns a vector embedding for the given text.
func (c *teiClient) Embed(ctx context.Context, text string) ([]float32, error) {
	results, err := c.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("TEI returned empty response for single input")
	}
	return results[0], nil
}

// EmbedBatch returns vector embeddings for multiple texts, automatically
// chunking requests to respect the TEI server's maximum batch size.
func (c *teiClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	allEmbeddings := make([][]float32, 0, len(texts))

	for start := 0; start < len(texts); start += c.maxBatchSize {
		end := min(start+c.maxBatchSize, len(texts))
		chunk := texts[start:end]

		embeddings, err := c.embedChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}
		allEmbeddings = append(allEmbeddings, embeddings...)
	}

	slog.Debug("TEI embedding batch completed",
		"inputs", len(texts), "chunks", (len(texts)+c.maxBatchSize-1)/c.maxBatchSize,
		"dimensions", len(allEmbeddings[0]))

	return allEmbeddings, nil
}

// embedChunk sends a single batch of texts to the TEI /embed endpoint.
func (c *teiClient) embedChunk(ctx context.Context, texts []string) ([][]float32, error) {
	reqBody := embedRequest{
		Inputs:   texts,
		Truncate: true,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal TEI request: %w", err)
	}

	url := c.baseURL + embedPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create TEI request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req) // #nosec G704 -- URL is built from the configured TEI base URL
	if err != nil {
		return nil, fmt.Errorf("TEI request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("TEI returned status %d: %s", resp.StatusCode, string(body))
	}

	var embeddings [][]float32
	if err := json.NewDecoder(resp.Body).Decode(&embeddings); err != nil {
		return nil, fmt.Errorf("failed to decode TEI response: %w", err)
	}

	if len(embeddings) != len(texts) {
		return nil, fmt.Errorf("TEI returned %d embeddings for %d inputs", len(embeddings), len(texts))
	}

	return embeddings, nil
}

// Close is a no-op for the TEI client.
func (*teiClient) Close() error {
	return nil
}
