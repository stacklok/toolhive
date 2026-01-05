package awssts

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// emptySHA256 is the SHA-256 hash of an empty string.
const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func TestNewSigner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		region      string
		service     string
		wantRegion  string
		wantService string
	}{
		{
			name:        "with custom service",
			region:      "us-east-1",
			service:     "custom-service",
			wantRegion:  "us-east-1",
			wantService: "custom-service",
		},
		{
			name:        "with empty service uses default",
			region:      "eu-west-1",
			service:     "",
			wantRegion:  "eu-west-1",
			wantService: DefaultService,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			signer := NewSigner(tt.region, tt.service)

			if signer.GetRegion() != tt.wantRegion {
				t.Errorf("GetRegion() = %v, want %v", signer.GetRegion(), tt.wantRegion)
			}
			if signer.GetService() != tt.wantService {
				t.Errorf("GetService() = %v, want %v", signer.GetService(), tt.wantService)
			}
		})
	}
}

//nolint:paralleltest // Tests share signer and credentials state
func TestSigner_SignRequest(t *testing.T) {
	ctx := context.Background()
	signer := NewSigner("us-east-1", "aws-mcp")

	validCreds := &Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		SessionToken:    "session-token",
		Expiration:      time.Now().Add(time.Hour),
	}

	t.Run("signs request with body", func(t *testing.T) {
		body := `{"method": "tools/list"}`
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://aws-mcp.us-east-1.api.aws/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		err := signer.SignRequest(ctx, req, validCreds)
		if err != nil {
			t.Fatalf("SignRequest() error = %v", err)
		}

		// Check that required headers are set
		if req.Header.Get("Authorization") == "" {
			t.Error("Authorization header not set")
		}
		if req.Header.Get("X-Amz-Date") == "" {
			t.Error("X-Amz-Date header not set")
		}
		if req.Header.Get("X-Amz-Security-Token") == "" {
			t.Error("X-Amz-Security-Token header not set")
		}

		// Authorization should contain AWS4-HMAC-SHA256
		authHeader := req.Header.Get("Authorization")
		if !strings.Contains(authHeader, "AWS4-HMAC-SHA256") {
			t.Errorf("Authorization header missing signature algorithm: %v", authHeader)
		}
		if !strings.Contains(authHeader, "aws-mcp") {
			t.Errorf("Authorization header missing service name: %v", authHeader)
		}

		// Body should still be readable
		bodyBytes, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("Failed to read body after signing: %v", err)
		}
		if string(bodyBytes) != body {
			t.Errorf("Body after signing = %v, want %v", string(bodyBytes), body)
		}
	})

	t.Run("signs request without body", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://aws-mcp.us-east-1.api.aws/mcp", nil)

		err := signer.SignRequest(ctx, req, validCreds)
		if err != nil {
			t.Fatalf("SignRequest() error = %v", err)
		}

		if req.Header.Get("Authorization") == "" {
			t.Error("Authorization header not set for GET request")
		}
	})

	t.Run("signs request with empty body", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://aws-mcp.us-east-1.api.aws/mcp", http.NoBody)

		err := signer.SignRequest(ctx, req, validCreds)
		if err != nil {
			t.Fatalf("SignRequest() error = %v", err)
		}

		if req.Header.Get("Authorization") == "" {
			t.Error("Authorization header not set for empty body")
		}
	})

	t.Run("errors with nil credentials", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://aws-mcp.us-east-1.api.aws/mcp", nil)

		err := signer.SignRequest(ctx, req, nil)
		if err == nil {
			t.Error("SignRequest() expected error for nil credentials, got nil")
		}
	})
}

//nolint:paralleltest // Tests share signer state
func TestSigner_HashPayload(t *testing.T) {
	signer := NewSigner("us-east-1", "aws-mcp")

	t.Run("hashes body correctly", func(t *testing.T) {
		body := "test body content"
		req, _ := http.NewRequest("POST", "http://example.com", strings.NewReader(body))

		hash, bodyBytes, err := signer.hashPayload(req)
		if err != nil {
			t.Fatalf("hashPayload() error = %v", err)
		}

		// SHA-256 hex is 64 chars
		if len(hash) != 64 {
			t.Errorf("hash length = %d, want 64", len(hash))
		}

		if string(bodyBytes) != body {
			t.Errorf("bodyBytes = %v, want %v", string(bodyBytes), body)
		}

		// Verify same content produces same hash
		req2, _ := http.NewRequest("POST", "http://example.com", strings.NewReader(body))
		hash2, _, _ := signer.hashPayload(req2)
		if hash != hash2 {
			t.Errorf("hashPayload() not deterministic: %v != %v", hash, hash2)
		}
	})

	t.Run("handles nil body", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://example.com", nil)

		hash, bodyBytes, err := signer.hashPayload(req)
		if err != nil {
			t.Fatalf("hashPayload() error = %v", err)
		}

		// Empty string SHA-256
		if hash != emptySHA256 {
			t.Errorf("hash for nil body = %v, want %v", hash, emptySHA256)
		}

		if bodyBytes != nil {
			t.Errorf("bodyBytes for nil body = %v, want nil", bodyBytes)
		}
	})

	t.Run("handles http.NoBody", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://example.com", http.NoBody)

		hash, bodyBytes, err := signer.hashPayload(req)
		if err != nil {
			t.Fatalf("hashPayload() error = %v", err)
		}

		if hash != emptySHA256 {
			t.Errorf("hash for http.NoBody = %v, want %v", hash, emptySHA256)
		}

		if bodyBytes != nil {
			t.Errorf("bodyBytes for http.NoBody = %v, want nil", bodyBytes)
		}
	})

	t.Run("handles large body", func(t *testing.T) {
		// 1MB body
		largeBody := bytes.Repeat([]byte("x"), 1024*1024)
		req, _ := http.NewRequest("POST", "http://example.com", bytes.NewReader(largeBody))

		hash, bodyBytes, err := signer.hashPayload(req)
		if err != nil {
			t.Fatalf("hashPayload() error = %v", err)
		}

		if len(hash) != 64 {
			t.Errorf("hash length = %d, want 64", len(hash))
		}

		if len(bodyBytes) != len(largeBody) {
			t.Errorf("bodyBytes length = %d, want %d", len(bodyBytes), len(largeBody))
		}
	})
}

func TestSigner_ContentLengthPreserved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	signer := NewSigner("us-east-1", "aws-mcp")
	creds := &Credentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Expiration:      time.Now().Add(time.Hour),
	}

	body := `{"test": "data"}`
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.com/api", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	err := signer.SignRequest(ctx, req, creds)
	if err != nil {
		t.Fatalf("SignRequest() error = %v", err)
	}

	if req.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(body))
	}
}
