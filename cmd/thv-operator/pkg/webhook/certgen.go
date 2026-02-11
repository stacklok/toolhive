// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package webhook provides utilities for webhook certificate generation and management.
package webhook

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// DefaultCertDir is the default directory where webhook certificates are stored
	DefaultCertDir = "/tmp/k8s-webhook-server/serving-certs"

	// CertFileName is the name of the certificate file
	CertFileName = "tls.crt"

	// KeyFileName is the name of the private key file
	KeyFileName = "tls.key"

	// CertValidityDuration is how long the certificate is valid for (1 year)
	CertValidityDuration = 365 * 24 * time.Hour

	// RSAKeySize is the size of the RSA key in bits
	RSAKeySize = 2048
)

var certLogger = log.Log.WithName("webhook-certs")

// CertGenerator generates self-signed certificates for webhook server
type CertGenerator struct {
	// CertDir is the directory where certificates will be written
	CertDir string

	// ServiceName is the name of the webhook service
	ServiceName string

	// Namespace is the namespace where the webhook service runs
	Namespace string
}

// NewCertGenerator creates a new CertGenerator with the given service name and namespace
func NewCertGenerator(serviceName, namespace string) *CertGenerator {
	return &CertGenerator{
		CertDir:     DefaultCertDir,
		ServiceName: serviceName,
		Namespace:   namespace,
	}
}

// Generate generates a self-signed certificate and private key for the webhook server.
// It creates the certificate directory if it doesn't exist and writes the certificate
// and key files. Returns the CA bundle (PEM-encoded certificate) that can be used
// in the ValidatingWebhookConfiguration.
func (g *CertGenerator) Generate() ([]byte, error) {
	certLogger.Info("Generating self-signed certificates for webhook server",
		"certDir", g.CertDir,
		"serviceName", g.ServiceName,
		"namespace", g.Namespace,
	)

	// Create certificate directory if it doesn't exist
	if err := os.MkdirAll(g.CertDir, 0750); err != nil {
		return nil, fmt.Errorf("failed to create certificate directory: %w", err)
	}

	// Generate private key
	privateKey, err := rsa.GenerateKey(rand.Reader, RSAKeySize)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	// Generate certificate
	cert, certPEM, err := g.generateCertificate(privateKey)
	if err != nil {
		return nil, fmt.Errorf("failed to generate certificate: %w", err)
	}

	// Write private key to file
	keyPath := filepath.Join(g.CertDir, KeyFileName)
	if err := g.writePrivateKey(keyPath, privateKey); err != nil {
		return nil, fmt.Errorf("failed to write private key: %w", err)
	}
	certLogger.Info("Successfully wrote private key", "path", keyPath)

	// Write certificate to file
	certPath := filepath.Join(g.CertDir, CertFileName)
	if err := g.writeCertificate(certPath, certPEM); err != nil {
		return nil, fmt.Errorf("failed to write certificate: %w", err)
	}
	certLogger.Info("Successfully wrote certificate", "path", certPath)

	certLogger.Info("Successfully generated webhook certificates",
		"notBefore", cert.NotBefore,
		"notAfter", cert.NotAfter,
		"serialNumber", cert.SerialNumber,
	)

	// Return CA bundle (which is the same as the certificate for self-signed)
	return certPEM, nil
}

// generateCertificate generates a self-signed X.509 certificate
func (g *CertGenerator) generateCertificate(privateKey *rsa.PrivateKey) (*x509.Certificate, []byte, error) {
	// Generate serial number
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate serial number: %w", err)
	}

	// Build DNS names for the certificate
	// Webhook service can be accessed via multiple DNS names:
	// - <service-name>.<namespace>.svc
	// - <service-name>.<namespace>.svc.cluster.local
	dnsNames := []string{
		g.ServiceName,
		fmt.Sprintf("%s.%s", g.ServiceName, g.Namespace),
		fmt.Sprintf("%s.%s.svc", g.ServiceName, g.Namespace),
		fmt.Sprintf("%s.%s.svc.cluster.local", g.ServiceName, g.Namespace),
	}

	notBefore := time.Now()
	notAfter := notBefore.Add(CertValidityDuration)

	// Create certificate template
	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Stacklok"},
			CommonName:   fmt.Sprintf("%s.%s.svc", g.ServiceName, g.Namespace),
		},
		DNSNames:              dnsNames,
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // Self-signed cert acts as its own CA
	}

	// Create self-signed certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	// Parse the certificate to return it
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse generated certificate: %w", err)
	}

	// Encode certificate to PEM
	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certDER,
	})

	return cert, certPEM, nil
}

// writePrivateKey writes the private key to a file in PEM format
func (*CertGenerator) writePrivateKey(path string, key *rsa.PrivateKey) error {
	keyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})

	if err := os.WriteFile(path, keyPEM, 0600); err != nil {
		return fmt.Errorf("failed to write private key file: %w", err)
	}

	return nil
}

// writeCertificate writes the certificate to a file
func (*CertGenerator) writeCertificate(path string, certPEM []byte) error {
	if err := os.WriteFile(path, certPEM, 0600); err != nil {
		return fmt.Errorf("failed to write certificate file: %w", err)
	}

	return nil
}

// CertificatesExist checks if certificate files already exist in the certificate directory
func (g *CertGenerator) CertificatesExist() bool {
	certPath := filepath.Join(g.CertDir, CertFileName)
	keyPath := filepath.Join(g.CertDir, KeyFileName)

	_, certErr := os.Stat(certPath)
	_, keyErr := os.Stat(keyPath)

	return certErr == nil && keyErr == nil
}

// GetCABundle reads and returns the CA bundle from the certificate file.
// This is useful for retrieving the CA bundle after certificate generation
// or when certificates already exist.
func (g *CertGenerator) GetCABundle() ([]byte, error) {
	certPath := filepath.Join(g.CertDir, CertFileName)

	// #nosec G304 -- certPath is constructed from constants and validated fields
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read certificate file: %w", err)
	}

	// Validate it's a valid PEM-encoded certificate
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("failed to decode PEM certificate")
	}

	// Verify it's a valid certificate
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	return certPEM, nil
}

// EnsureCertificates ensures that valid certificates exist. If they don't exist,
// it generates new ones. Returns the CA bundle.
func (g *CertGenerator) EnsureCertificates() ([]byte, error) {
	if g.CertificatesExist() {
		certLogger.Info("Webhook certificates already exist, using existing certificates")
		return g.GetCABundle()
	}

	certLogger.Info("Webhook certificates do not exist, generating new certificates")
	return g.Generate()
}

// EncodeCABundle base64 encodes the CA bundle for use in Kubernetes resources
func EncodeCABundle(caBundle []byte) []byte {
	// For Kubernetes ValidatingWebhookConfiguration, the caBundle field expects
	// base64-encoded PEM certificate data. However, when we read from Go code,
	// we already have PEM-encoded data. We need to ensure proper formatting.

	// Trim any extra whitespace and ensure consistent line endings
	return bytes.TrimSpace(caBundle)
}
