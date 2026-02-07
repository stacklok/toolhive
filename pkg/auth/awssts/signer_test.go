// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package awssts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEmptySHA256IsCorrect(t *testing.T) {
	t.Parallel()
	h := sha256.Sum256([]byte(""))
	assert.Equal(t, hex.EncodeToString(h[:]), emptySHA256)
}

func Test_newRequestSigner(t *testing.T) {
	t.Parallel()

	t.Run("succeeds with valid region", func(t *testing.T) {
		t.Parallel()
		s, err := newRequestSigner("us-east-1")
		require.NoError(t, err)
		require.NotNil(t, s)
	})

	t.Run("succeeds with custom service", func(t *testing.T) {
		t.Parallel()
		s, err := newRequestSigner("eu-west-1", withService("custom-service"))
		require.NoError(t, err)
		require.NotNil(t, s)
	})

	t.Run("fails with empty region", func(t *testing.T) {
		t.Parallel()
		_, err := newRequestSigner("")
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMissingRegion)
	})
}

//nolint:paralleltest // Tests share signer and credentials state
func TestSigner_SignRequest(t *testing.T) {
	ctx := context.Background()
	signer, err := newRequestSigner("us-east-1")
	require.NoError(t, err)

	validCreds := &aws.Credentials{
		AccessKeyID:     "AKIAIOSFODNN7EXAMPLE",
		SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		SessionToken:    "session-token",
		Expires:         time.Now().Add(time.Hour),
		CanExpire:       true,
	}

	t.Run("signs request with body", func(t *testing.T) {
		body := `{"method": "tools/list"}`
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://aws-mcp.us-east-1.api.aws/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")

		err := signer.SignRequest(ctx, req, validCreds)
		require.NoError(t, err)

		assert.NotEmpty(t, req.Header.Get("Authorization"))
		assert.NotEmpty(t, req.Header.Get("X-Amz-Date"))
		assert.NotEmpty(t, req.Header.Get("X-Amz-Security-Token"))

		authHeader := req.Header.Get("Authorization")
		assert.Contains(t, authHeader, "AWS4-HMAC-SHA256")
		assert.Contains(t, authHeader, "aws-mcp")

		// Body should still be readable
		bodyBytes, err := io.ReadAll(req.Body)
		require.NoError(t, err)
		assert.Equal(t, body, string(bodyBytes))
	})

	t.Run("signs request without body", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://aws-mcp.us-east-1.api.aws/mcp", nil)

		err := signer.SignRequest(ctx, req, validCreds)
		require.NoError(t, err)
		assert.NotEmpty(t, req.Header.Get("Authorization"))
	})

	t.Run("signs request with empty body", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://aws-mcp.us-east-1.api.aws/mcp", http.NoBody)

		err := signer.SignRequest(ctx, req, validCreds)
		require.NoError(t, err)
		assert.NotEmpty(t, req.Header.Get("Authorization"))
	})

	t.Run("errors with nil credentials", func(t *testing.T) {
		req, _ := http.NewRequestWithContext(ctx, "POST", "https://aws-mcp.us-east-1.api.aws/mcp", nil)

		err := signer.SignRequest(ctx, req, nil)
		require.Error(t, err)
	})
}

//nolint:paralleltest // Tests share signer state
func TestSigner_HashPayload(t *testing.T) {
	signer, err := newRequestSigner("us-east-1")
	require.NoError(t, err)

	t.Run("hashes body correctly", func(t *testing.T) {
		body := "test body content"
		req, _ := http.NewRequest("POST", "http://example.com", strings.NewReader(body))

		hash, bodyBytes, err := signer.hashPayload(req)
		require.NoError(t, err)
		assert.Len(t, hash, 64)
		assert.Equal(t, body, string(bodyBytes))

		// Verify same content produces same hash
		req2, _ := http.NewRequest("POST", "http://example.com", strings.NewReader(body))
		hash2, _, err := signer.hashPayload(req2)
		require.NoError(t, err)
		assert.Equal(t, hash, hash2)
	})

	t.Run("handles nil body", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://example.com", nil)

		hash, bodyBytes, err := signer.hashPayload(req)
		require.NoError(t, err)
		assert.Equal(t, emptySHA256, hash)
		assert.Nil(t, bodyBytes)
	})

	t.Run("handles http.NoBody", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "http://example.com", http.NoBody)

		hash, bodyBytes, err := signer.hashPayload(req)
		require.NoError(t, err)
		assert.Equal(t, emptySHA256, hash)
		assert.Nil(t, bodyBytes)
	})

	t.Run("handles large body within limit", func(t *testing.T) {
		// 1MB body (well within 10MB limit)
		largeBody := bytes.Repeat([]byte("x"), 1024*1024)
		req, _ := http.NewRequest("POST", "http://example.com", bytes.NewReader(largeBody))

		hash, bodyBytes, err := signer.hashPayload(req)
		require.NoError(t, err)
		assert.Len(t, hash, 64)
		assert.Len(t, bodyBytes, len(largeBody))
	})

	t.Run("rejects body exceeding size limit", func(t *testing.T) {
		oversizedBody := bytes.Repeat([]byte("x"), maxPayloadSize+1)
		req, _ := http.NewRequest("POST", "http://example.com", bytes.NewReader(oversizedBody))

		_, _, err := signer.hashPayload(req)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum size")
	})
}

func TestSigner_ContentLengthPreserved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	signer, err := newRequestSigner("us-east-1")
	require.NoError(t, err)

	creds := &aws.Credentials{
		AccessKeyID:     "AKIATEST",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Expires:         time.Now().Add(time.Hour),
		CanExpire:       true,
	}

	body := `{"test": "data"}`
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://example.com/api", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	err = signer.SignRequest(ctx, req, creds)
	require.NoError(t, err)
	assert.Equal(t, int64(len(body)), req.ContentLength)
}
