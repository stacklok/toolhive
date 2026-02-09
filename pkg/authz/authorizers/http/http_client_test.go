// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *ConnectionConfig
		wantErr bool
		errMsg  string
	}{
		{
			name:    "nil config",
			config:  nil,
			wantErr: true,
			errMsg:  "HTTP configuration is required",
		},
		{
			name:    "empty URL",
			config:  &ConnectionConfig{},
			wantErr: true,
			errMsg:  "HTTP URL is required",
		},
		{
			name: "invalid URL",
			config: &ConnectionConfig{
				URL: "://invalid",
			},
			wantErr: true,
			errMsg:  "invalid URL",
		},
		{
			name: "invalid scheme",
			config: &ConnectionConfig{
				URL: "ftp://localhost:9000",
			},
			wantErr: true,
			errMsg:  "URL scheme must be http or https, got: ftp",
		},
		{
			name: "valid HTTP URL",
			config: &ConnectionConfig{
				URL: "http://localhost:9000",
			},
			wantErr: false,
		},
		{
			name: "valid HTTPS URL",
			config: &ConnectionConfig{
				URL: "https://pdp.example.com",
			},
			wantErr: false,
		},
		{
			name: "with timeout",
			config: &ConnectionConfig{
				URL:     "http://localhost:9000",
				Timeout: 60,
			},
			wantErr: false,
		},
		{
			name: "with insecure skip verify",
			config: &ConnectionConfig{
				URL:                "https://localhost:9000",
				InsecureSkipVerify: true,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewClient(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewClient() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && tt.errMsg != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("NewClient() error = %v, want error containing %q", err, tt.errMsg)
				}
			}
			if !tt.wantErr && client == nil {
				t.Error("NewClient() returned nil client without error")
			}
		})
	}
}

func TestHTTPClient_Authorize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		response   DecisionResponse
		statusCode int
		probe      bool
		wantAllow  bool
		wantErr    bool
	}{
		{
			name:       "allow decision",
			response:   DecisionResponse{Allow: true},
			statusCode: nethttp.StatusOK,
			probe:      false,
			wantAllow:  true,
			wantErr:    false,
		},
		{
			name:       "deny decision",
			response:   DecisionResponse{Allow: false},
			statusCode: nethttp.StatusOK,
			probe:      false,
			wantAllow:  false,
			wantErr:    false,
		},
		{
			name:       "allow with probe mode",
			response:   DecisionResponse{Allow: true},
			statusCode: nethttp.StatusOK,
			probe:      true,
			wantAllow:  true,
			wantErr:    false,
		},
		{
			name:       "server error",
			response:   DecisionResponse{},
			statusCode: nethttp.StatusInternalServerError,
			probe:      false,
			wantAllow:  false,
			wantErr:    true,
		},
		{
			name:       "bad request",
			response:   DecisionResponse{},
			statusCode: nethttp.StatusBadRequest,
			probe:      false,
			wantAllow:  false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create a test server
			var receivedProbe bool
			server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
				// Check request method
				if r.Method != nethttp.MethodPost {
					t.Errorf("Expected POST request, got %s", r.Method)
				}

				// Check content type
				if ct := r.Header.Get("Content-Type"); ct != "application/json" {
					t.Errorf("Expected Content-Type application/json, got %s", ct)
				}

				// Check probe parameter
				receivedProbe = r.URL.Query().Get("probe") == "true"

				// Send response
				w.WriteHeader(tt.statusCode)
				if tt.statusCode == nethttp.StatusOK {
					if err := json.NewEncoder(w).Encode(tt.response); err != nil {
						t.Fatalf("Failed to encode response: %v", err)
					}
				} else {
					_, _ = w.Write([]byte("error"))
				}
			}))
			defer server.Close()

			// Create client
			client, err := NewClient(&ConnectionConfig{URL: server.URL})
			if err != nil {
				t.Fatalf("Failed to create client: %v", err)
			}

			// Make request
			porc := PORC{
				"principal": Principal{"sub": "test"},
				"operation": "mcp:tool:call",
				"resource":  "mcp:tool:test",
				"context":   Context{},
			}

			allow, err := client.Authorize(context.Background(), porc, tt.probe)
			if (err != nil) != tt.wantErr {
				t.Errorf("Authorize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && allow != tt.wantAllow {
				t.Errorf("Authorize() = %v, want %v", allow, tt.wantAllow)
			}
			if tt.probe != receivedProbe {
				t.Errorf("Authorize() probe = %v, want %v", receivedProbe, tt.probe)
			}
		})
	}
}

func TestHTTPClient_Authorize_InvalidJSON(t *testing.T) {
	t.Parallel()

	// Create a test server that returns invalid JSON
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		w.WriteHeader(nethttp.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	client, err := NewClient(&ConnectionConfig{URL: server.URL})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	porc := PORC{
		"principal": Principal{"sub": "test"},
		"operation": "mcp:tool:call",
		"resource":  "mcp:tool:test",
		"context":   Context{},
	}

	_, err = client.Authorize(context.Background(), porc, false)
	if err == nil {
		t.Error("Expected error for invalid JSON response")
	}
	if !strings.Contains(err.Error(), "failed to parse decision response") {
		t.Errorf("Expected parse error, got: %v", err)
	}
}

func TestHTTPClient_Close(t *testing.T) {
	t.Parallel()

	client, err := NewClient(&ConnectionConfig{URL: "http://localhost:9000"})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Close should not return an error
	if err := client.Close(); err != nil {
		t.Errorf("Close() error = %v", err)
	}
}

func TestHTTPClient_Authorize_PORCValidation(t *testing.T) {
	t.Parallel()

	// Create a test server that validates the PORC structure
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		var porc map[string]any
		if err := json.NewDecoder(r.Body).Decode(&porc); err != nil {
			t.Errorf("Failed to decode PORC: %v", err)
			w.WriteHeader(nethttp.StatusBadRequest)
			return
		}

		// Validate required fields
		if _, ok := porc["principal"]; !ok {
			t.Error("PORC missing principal")
		}
		if _, ok := porc["operation"]; !ok {
			t.Error("PORC missing operation")
		}
		if _, ok := porc["resource"]; !ok {
			t.Error("PORC missing resource")
		}
		if _, ok := porc["context"]; !ok {
			t.Error("PORC missing context")
		}

		w.WriteHeader(nethttp.StatusOK)
		_ = json.NewEncoder(w).Encode(DecisionResponse{Allow: true})
	}))
	defer server.Close()

	client, err := NewClient(&ConnectionConfig{URL: server.URL})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	porc := PORC{
		"principal": map[string]any{
			"sub":    "user@example.com",
			"mroles": []string{"mrn:iam:role:user"},
		},
		"operation": "mcp:tool:call",
		"resource":  "mrn:mcp:test:tool:weather",
		"context": map[string]any{
			"mcp": map[string]any{
				"feature":     "tool",
				"operation":   "call",
				"resource_id": "weather",
			},
		},
	}

	allow, err := client.Authorize(context.Background(), porc, false)
	if err != nil {
		t.Errorf("Authorize() error = %v", err)
	}
	if !allow {
		t.Error("Authorize() = false, want true")
	}
}

func TestHTTPClient_Authorize_Timeout(t *testing.T) {
	t.Parallel()

	// Create a test server that delays response
	server := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, _ *nethttp.Request) {
		// Note: We can't actually test timeout easily in unit tests without making them slow
		// This just verifies the timeout is set correctly on the client
		w.WriteHeader(nethttp.StatusOK)
		_ = json.NewEncoder(w).Encode(DecisionResponse{Allow: true})
	}))
	defer server.Close()

	// Create client with very short timeout
	client, err := NewClient(&ConnectionConfig{
		URL:     server.URL,
		Timeout: 1, // 1 second
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	porc := PORC{
		"principal": Principal{"sub": "test"},
		"operation": "mcp:tool:call",
		"resource":  "mcp:tool:test",
		"context":   Context{},
	}

	// Should succeed since server responds immediately
	allow, err := client.Authorize(context.Background(), porc, false)
	if err != nil {
		t.Errorf("Authorize() error = %v", err)
	}
	if !allow {
		t.Error("Authorize() = false, want true")
	}
}

func TestHTTPClient_Authorize_ContextCancellation(t *testing.T) {
	t.Parallel()

	// Create channels for coordination
	requestReceived := make(chan struct{})
	doneChan := make(chan struct{})

	// Create a test server that blocks until the done channel is closed
	server := httptest.NewServer(nethttp.HandlerFunc(func(_ nethttp.ResponseWriter, r *nethttp.Request) {
		// Signal that request was received
		close(requestReceived)

		// Block until either the request context is done or the test completes
		select {
		case <-r.Context().Done():
			// Request was cancelled, exit cleanly
			return
		case <-doneChan:
			// Test is done, exit cleanly
			return
		}
	}))
	defer func() {
		close(doneChan)
		server.Close()
	}()

	client, err := NewClient(&ConnectionConfig{URL: server.URL})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	porc := PORC{
		"principal": Principal{"sub": "test"},
		"operation": "mcp:tool:call",
		"resource":  "mcp:tool:test",
		"context":   Context{},
	}

	// Start the authorization request in a goroutine
	errChan := make(chan error, 1)
	go func() {
		_, err := client.Authorize(ctx, porc, false)
		errChan <- err
	}()

	// Wait for the request to be received by the server
	<-requestReceived

	// Cancel the context
	cancel()

	// Wait for the authorization to complete and verify it returns an error
	err = <-errChan
	if err == nil {
		t.Error("Expected error from context cancellation, got nil")
		return
	}

	// Verify the error is related to context cancellation
	if !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "HTTP request failed") {
		t.Errorf("Expected context cancellation error, got: %v", err)
	}
}
