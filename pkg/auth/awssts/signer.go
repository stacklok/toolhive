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

// Signer signs HTTP requests using AWS Signature Version 4.
//
// SigV4 signing must be the last middleware in the chain before sending
// the request, as any modification to headers after signing will invalidate
// the signature.
type Signer struct {
	signer  *v4.Signer
	region  string
	service string
}

// NewSigner creates a new SigV4 signer for the specified region and service.
//
// Parameters:
//   - region: AWS region (e.g., "us-east-1")
//   - service: AWS service name for SigV4 (default: "aws-mcp" for AWS MCP Server)
func NewSigner(region, service string) *Signer {
	if service == "" {
		service = DefaultService
	}
	return &Signer{
		signer:  v4.NewSigner(),
		region:  region,
		service: service,
	}
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
func (s *Signer) SignRequest(ctx context.Context, req *http.Request, creds *Credentials) error {
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

	// Create AWS credentials for the signer
	awsCreds := aws.Credentials{
		AccessKeyID:     creds.AccessKeyID,
		SecretAccessKey: creds.SecretAccessKey,
		SessionToken:    creds.SessionToken,
	}

	// Sign the request
	err = s.signer.SignHTTP(ctx, awsCreds, req, payloadHash, s.service, s.region, time.Now())
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
func (*Signer) hashPayload(req *http.Request) (string, []byte, error) {
	// Handle empty body
	if req.Body == nil || req.Body == http.NoBody {
		// SHA-256 of empty string
		return "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", nil, nil
	}

	// Read the body
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return "", nil, err
	}

	// Close the original body
	if err := req.Body.Close(); err != nil {
		return "", nil, err
	}

	// Hash the body
	hash := sha256.Sum256(bodyBytes)
	return hex.EncodeToString(hash[:]), bodyBytes, nil
}

// GetRegion returns the configured AWS region.
func (s *Signer) GetRegion() string {
	return s.region
}

// GetService returns the configured AWS service name.
func (s *Signer) GetService() string {
	return s.service
}
