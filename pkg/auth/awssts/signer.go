// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package awssts provides AWS STS token exchange and SigV4 signing functionality.
package awssts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// maxPayloadSize is the maximum request body size (10 MB) for SigV4 signing.
const maxPayloadSize = 10 * 1024 * 1024

// defaultService is the AWS service name used in SigV4 signing for AWS MCP Server.
// This value appears in the credential scope of the Authorization header:
//
//	Credential=AKIAEXAMPLE/20260206/us-east-1/aws-mcp/aws4_request
//
// The service name must match what AWS expects. For AWS MCP Server, this is "aws-mcp",
// as documented in the IAM actions (aws-mcp:InvokeMcp, aws-mcp:CallReadOnlyTool, etc.)
// and the endpoint URL pattern (aws-mcp.{region}.api.aws).
//
// See: https://docs.aws.amazon.com/aws-mcp/latest/userguide/getting-started-aws-mcp-server.html
const defaultService = "aws-mcp"

// requestSigner signs HTTP requests using AWS Signature Version 4.
//
// SigV4 signing must be the last middleware in the chain before sending
// the request, as any modification to headers after signing will invalidate
// the signature.
type requestSigner struct {
	signer  *v4.Signer
	region  string
	service string
}

type signerOption func(*requestSigner)

// withService sets a custom service name for SigV4 signing.
func withService(service string) signerOption {
	return func(s *requestSigner) {
		s.service = service
	}
}

// newRequestSigner creates a new SigV4 request signer for the specified region.
//
// By default, it uses "aws-mcp" as the service name for AWS MCP Server.
// Use withService to override for other AWS services.
func newRequestSigner(region string, opts ...signerOption) (*requestSigner, error) {
	if region == "" {
		return nil, ErrMissingRegion
	}

	s := &requestSigner{
		signer:  v4.NewSigner(),
		region:  region,
		service: defaultService,
	}

	for _, opt := range opts {
		opt(s)
	}

	return s, nil
}

// SignRequest signs an HTTP request using AWS SigV4.
//
// This method:
//  1. Reads and hashes the request body with SHA-256
//  2. Signs the request with the provided credentials
//  3. Adds required headers: Authorization, X-Amz-Date, X-Amz-Security-Token
//
// The request body is consumed and replaced with a new reader containing
// the same content, allowing the request to be sent after signing.
//
// Parameters:
//   - ctx: Context for the signing operation
//   - req: HTTP request to sign (will be modified in place)
//   - creds: AWS credentials from STS token exchange
//
// Returns an error if:
//   - The request body cannot be read
//   - Signing fails
func (s *requestSigner) SignRequest(ctx context.Context, req *http.Request, creds *aws.Credentials) error {
	if creds == nil {
		return fmt.Errorf("credentials are required for signing")
	}

	// Read and hash the request body
	payloadHash, bodyBytes, err := s.hashPayload(req)
	if err != nil {
		return fmt.Errorf("failed to hash request payload: %w", err)
	}

	// Replace the body with a new reader (the original was consumed)
	if bodyBytes != nil {
		req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	}

	// Sign the request
	err = s.signer.SignHTTP(ctx, *creds, req, payloadHash, s.service, s.region, time.Now())
	if err != nil {
		return fmt.Errorf("failed to sign request: %w", err)
	}

	return nil
}

// hashPayload reads and hashes the request body with SHA-256.
//
// Returns:
//   - payloadHash: Hex-encoded SHA-256 hash of the body
//   - bodyBytes: The body content (for replacing the consumed reader)
//   - error: Any error reading the body
func (*requestSigner) hashPayload(req *http.Request) (string, []byte, error) {
	// Handle empty body
	if req.Body == nil || req.Body == http.NoBody {
		// SHA-256 of empty string
		return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil, nil
	}

	// Read the body with a size limit to prevent memory exhaustion
	bodyBytes, err := io.ReadAll(io.LimitReader(req.Body, maxPayloadSize+1))
	if err != nil {
		return "", nil, err
	}
	if len(bodyBytes) > maxPayloadSize {
		return "", nil, fmt.Errorf("request body exceeds maximum size of %d bytes", maxPayloadSize)
	}

	// Close the original body
	if err := req.Body.Close(); err != nil {
		return "", nil, err
	}

	// Hash the body
	hash := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(hash[:]), bodyBytes, nil
}
