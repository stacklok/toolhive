// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package otlp

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"os"
)

// newTLSConfigFromCA creates a tls.Config that trusts certificates from the given
// CA bundle file path. The returned config appends the custom CAs to the system
// pool so both the custom CA and standard public CAs are trusted.
func newTLSConfigFromCA(caCertPath string) (*tls.Config, error) {
	caCert, err := os.ReadFile(caCertPath) // #nosec G304 - path comes from operator-controlled mount
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate bundle %q: %w", caCertPath, err)
	}

	caCertPool, err := x509.SystemCertPool()
	if err != nil {
		slog.Warn("System CA pool unavailable, using custom CA only", "error", err)
		caCertPool = x509.NewCertPool()
	}

	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA certificate bundle %q: no valid PEM certificates found", caCertPath)
	}

	return &tls.Config{
		RootCAs:    caCertPool,
		MinVersion: tls.VersionTLS12,
	}, nil
}
