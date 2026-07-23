// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"time"
)

const (
	// CAValidity is the bump-CA lifetime (ADR D9).
	CAValidity = 90 * 24 * time.Hour
	// rotationLead is how long before expiry rotation becomes due (50% of
	// validity, so the overlap window covers the backend rollout).
	rotationLead = CAValidity / 2
	// leafValidity bounds each minted per-SNI cert. Short-lived: the cert is
	// only ever validated by the co-located backend container against the
	// per-tenant bump CA.
	leafValidity = 24 * time.Hour
)

// BumpCA is the per-tenant TLS-bump certificate authority (D9). It signs
// per-SNI leaf certificates so the backend container's HTTPS connection can
// be terminated at the sidecar, inspected, and re-originated.
//
// The private key never leaves the operator-owned CA Secret / the sidecar
// process; it is never logged.
type BumpCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	// notAfter is carried explicitly so NeedsRotation is testable.
	notAfter time.Time
}

// GenerateBumpCA creates a self-signed ECDSA P-256 CA valid for CAValidity
// from now. commonName identifies the tenant (e.g. the MCPServer name).
func GenerateBumpCA(commonName string, now time.Time) (*BumpCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to generate bump CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(CAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to self-sign bump CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to parse self-signed bump CA: %w", err)
	}
	return &BumpCA{cert: cert, key: key, notAfter: cert.NotAfter}, nil
}

// ParseBumpCA loads a CA from PEM cert + PEM PKCS8/SEC1 private key bytes
// (the operator-owned Secret volume contents). Fails loudly on malformed or
// non-CA input — a broker that cannot mint certs must not start.
func ParseBumpCA(certPEM, keyPEM []byte) (*BumpCA, error) {
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("egressbroker: bump CA cert PEM is missing or not a CERTIFICATE block")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to parse bump CA cert: %w", err)
	}
	if !cert.IsCA {
		return nil, fmt.Errorf("egressbroker: bump CA cert is not a CA certificate")
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, fmt.Errorf("egressbroker: bump CA key PEM is missing")
	}
	key, err := parseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to parse bump CA key: %w", err)
	}
	return &BumpCA{cert: cert, key: key, notAfter: cert.NotAfter}, nil
}

// CertPEM renders the public CA certificate (the bundle content distributed
// to backend containers). Key material is never rendered by any accessor.
func (c *BumpCA) CertPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
}

// KeyPEM renders the CA private key in PKCS8 PEM form. Used ONLY by the
// operator-side reconcile to populate the bump-CA Secret; never log or
// include the result in errors.
func (c *BumpCA) KeyPEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(c.key)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to marshal bump CA key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// NeedsRotation reports whether the CA is within the rotation window (past
// 50% of validity) at now.
func (c *BumpCA) NeedsRotation(now time.Time) bool {
	return !now.Before(c.notAfter.Add(-rotationLead))
}

// NotAfter returns the CA certificate's expiry. The operator orders CA
// generations by it when deciding which generations to retain across a
// rotation.
func (c *BumpCA) NotAfter() time.Time {
	return c.notAfter
}

// MintLeaf issues a short-lived leaf certificate for hostname (the SNI the
// backend dialed), signed by this CA. hostname must be a DNS name, not an IP:
// D5/D7 allowlists are name-based and IP-literal TLS bumps are refused
// (fail closed).
func (c *BumpCA) MintLeaf(hostname string, now time.Time) (certPEM, keyPEM []byte, err error) {
	if hostname == "" {
		return nil, nil, fmt.Errorf("egressbroker: cannot mint leaf for empty hostname")
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return nil, nil, fmt.Errorf("egressbroker: refusing to mint leaf for IP-literal SNI")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("egressbroker: failed to generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, nil, fmt.Errorf("egressbroker: failed to sign leaf for %q: %w", hostname, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, fmt.Errorf("egressbroker: failed to marshal leaf key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to generate cert serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial = big.NewInt(1)
	}
	return serial, nil
}

func parseECPrivateKey(der []byte) (*ecdsa.PrivateKey, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		if ecKey, ok := key.(*ecdsa.PrivateKey); ok {
			return ecKey, nil
		}
		return nil, fmt.Errorf("key is not ECDSA")
	}
	return x509.ParseECPrivateKey(der)
}
