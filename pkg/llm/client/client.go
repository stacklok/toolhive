// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client is a minimal, dependency-free client for OpenAI-compatible
// chat-completion endpoints. It targets the widely-implemented
// POST /chat/completions shape, so it works with OpenAI, most LLM gateways,
// and local servers (Ollama, vLLM, llama.cpp) by varying the base URL — which
// also lets callers keep traffic on-host when sending data to a model is a
// concern.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Config configures a Client. Model and BaseURL are required; APIKey is
// optional so the client can target an auth-injecting proxy (e.g. the
// `thv llm` localhost proxy) or a keyless local model.
type Config struct {
	// Model is the model identifier (e.g. "gpt-4o-mini").
	Model string
	// BaseURL is the API root (e.g. "https://api.openai.com/v1"); the client
	// appends "/chat/completions".
	BaseURL string
	// APIKey, when non-empty, is sent as a Bearer token. Leave empty when the
	// endpoint injects auth itself (a proxy) or needs none (a local model).
	APIKey string
	// HTTPClient is optional; a 30s-timeout client is used when nil.
	HTTPClient *http.Client
}

// Client calls an OpenAI-compatible chat-completions endpoint.
type Client struct {
	model   string
	baseURL string
	apiKey  string
	http    *http.Client
}

// New validates the config and returns a Client.
func New(cfg Config) (*Client, error) {
	if cfg.Model == "" {
		return nil, errors.New("llm client: model is required")
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("llm client: base URL is required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		model:   cfg.Model,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		apiKey:  cfg.APIKey,
		http:    hc,
	}, nil
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Complete sends a single-turn (system + user) chat completion and returns the
// assistant message content. Temperature is fixed at 0 for reproducibility.
func (c *Client) Complete(ctx context.Context, system, user string) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		Temperature: 0,
	})
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("call llm: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_, _ = io.Copy(io.Discard, resp.Body) // drain remainder so the connection can be reused
		return "", fmt.Errorf("llm returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", errors.New("llm returned no choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
