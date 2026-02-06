// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package client provides an HTTP client for the ToolHive Skills API.
package client

// Client is an HTTP client for the ToolHive Skills API.
type Client struct {
	baseURL string
}

// NewClient creates a new Skills API client with the given base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
	}
}
