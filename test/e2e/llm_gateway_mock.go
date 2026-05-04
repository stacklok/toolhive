// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GatewayRequest records a single request received by the mock LLM gateway.
type GatewayRequest struct {
	Method string
	Path   string
	// Bearer is the token value stripped from the Authorization header,
	// or empty if no Authorization header was present.
	Bearer string
}

// LLMGatewayMock is a server that responds to OpenAI-compatible requests
// (/v1/models, /v1/chat/completions) and records every request it receives.
//
// Use NewLLMGatewayMock for HTTPS (self-signed cert) or NewLLMGatewayMockHTTP
// for plain HTTP. The HTTP variant is simpler for e2e tests where the thv
// subprocess cannot be easily configured to trust a self-signed cert.
type LLMGatewayMock struct {
	server  *http.Server
	port    int
	useTLS  bool
	certPEM []byte // non-nil only when useTLS is true

	mu       sync.Mutex
	requests []GatewayRequest
}

// NewLLMGatewayMock creates a mock LLM gateway that serves HTTPS with a
// self-signed certificate. Use CertPEM / TLSClientConfig to build a trusting
// HTTP client. Call Start to begin serving.
func NewLLMGatewayMock(port int) (*LLMGatewayMock, error) {
	certPEM, keyPEM, err := generateGatewayCert()
	if err != nil {
		return nil, err
	}

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("loading TLS key pair: %w", err)
	}

	m := &LLMGatewayMock{port: port, useTLS: true, certPEM: certPEM}
	m.server = m.newServer()
	m.server.TLSConfig = &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	return m, nil
}

// NewLLMGatewayMockHTTP creates a mock LLM gateway that serves plain HTTP.
// Useful in e2e tests where the thv subprocess cannot easily be configured to
// trust a self-signed certificate. Call Start to begin serving.
func NewLLMGatewayMockHTTP(port int) *LLMGatewayMock {
	m := &LLMGatewayMock{port: port, useTLS: false}
	m.server = m.newServer()
	return m
}

func (m *LLMGatewayMock) newServer() *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", m.handleModels)
	mux.HandleFunc("/v1/chat/completions", m.handleChatCompletions)
	// Gemini CLI sends requests to /v1beta/models/{model}:generateContent.
	// The mock handles any /v1beta/ path so proxy routing tests can verify
	// end-to-end request flow without a real Envoy AI Gateway.
	mux.HandleFunc("/v1beta/", m.handleGeminiGenerate)
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", m.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// Start begins serving requests. It blocks briefly until the port is open.
func (m *LLMGatewayMock) Start() error {
	errCh := make(chan error, 1)
	go func() {
		var err error
		if m.useTLS {
			err = m.server.ListenAndServeTLS("", "")
		} else {
			err = m.server.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", m.port)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case err := <-errCh:
			return err
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
	return fmt.Errorf("mock LLM gateway did not start on port %d within 5s", m.port)
}

// Stop shuts down the mock gateway.
func (m *LLMGatewayMock) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return m.server.Shutdown(ctx)
}

// URL returns the base URL of the mock gateway (https:// or http:// depending
// on how the mock was created).
func (m *LLMGatewayMock) URL() string {
	scheme := "https"
	if !m.useTLS {
		scheme = "http"
	}
	return fmt.Sprintf("%s://localhost:%d", scheme, m.port)
}

// CertPEM returns the PEM-encoded self-signed certificate. Use this to build a
// custom *http.Client or x509.CertPool that trusts the mock gateway.
func (m *LLMGatewayMock) CertPEM() []byte {
	return m.certPEM
}

// TLSClientConfig returns a *tls.Config that trusts the mock gateway's
// self-signed certificate. Useful for building a custom *http.Client in tests.
func (m *LLMGatewayMock) TLSClientConfig() (*tls.Config, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(m.certPEM) {
		return nil, fmt.Errorf("failed to parse mock gateway certificate")
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil //nolint:gosec // test-only; min version set
}

// Requests returns a copy of all requests received so far, in order.
func (m *LLMGatewayMock) Requests() []GatewayRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GatewayRequest, len(m.requests))
	copy(out, m.requests)
	return out
}

// LastBearerToken returns the Bearer token from the most recent request, or
// empty string if no requests have been received or none carried a token.
func (m *LLMGatewayMock) LastBearerToken() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.requests) - 1; i >= 0; i-- {
		if m.requests[i].Bearer != "" {
			return m.requests[i].Bearer
		}
	}
	return ""
}

// record saves a request observation.
func (m *LLMGatewayMock) record(r *http.Request) {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if bearer == r.Header.Get("Authorization") {
		bearer = "" // no "Bearer " prefix → no token
	}
	m.mu.Lock()
	m.requests = append(m.requests, GatewayRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Bearer: bearer,
	})
	m.mu.Unlock()
}

func (m *LLMGatewayMock) handleModels(w http.ResponseWriter, r *http.Request) {
	m.record(r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data": []map[string]any{
			{"id": "mock-gpt-4", "object": "model", "created": 1700000000, "owned_by": "mock"},
			{"id": "mock-claude-3", "object": "model", "created": 1700000000, "owned_by": "mock"},
		},
	})
}

func (m *LLMGatewayMock) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	m.record(r)

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	model := "mock-gpt-4"
	if v, ok := body["model"].(string); ok {
		model = v
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-mock-001",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Hello from the mock LLM gateway!",
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30,
		},
	})
}

// handleGeminiGenerate handles Gemini CLI's native API path:
// /v1beta/models/{model}:generateContent (and other /v1beta/* variants).
// It returns a minimal Gemini GenerateContent response so proxy routing tests
// can verify end-to-end flow without a real Envoy AI Gateway.
func (m *LLMGatewayMock) handleGeminiGenerate(w http.ResponseWriter, r *http.Request) {
	m.record(r)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"candidates": []map[string]any{{
			"content": map[string]any{
				"parts": []map[string]any{{"text": "Hello from the mock LLM gateway!"}},
				"role":  "model",
			},
			"finishReason": "STOP",
			"index":        0,
		}},
		"usageMetadata": map[string]any{
			"promptTokenCount":     10,
			"candidatesTokenCount": 20,
			"totalTokenCount":      30,
		},
	})
}

// generateGatewayCert creates a self-signed ECDSA certificate for localhost.
func generateGatewayCert() (certPEM, keyPEM []byte, err error) {
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generating key: %w", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"thv-llm-mock"}},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("creating certificate: %w", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshaling key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return certPEM, keyPEM, nil
}
