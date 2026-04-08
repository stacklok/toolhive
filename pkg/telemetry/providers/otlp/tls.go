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

func buildTLSConfig(caCertFile string) (*tls.Config, error) {
	certPool, err := x509.SystemCertPool()
	if err != nil {
		slog.Debug("failed to load system cert pool, using empty pool", "error", err)
		certPool = x509.NewCertPool()
	}

	pemData, err := os.ReadFile(caCertFile) //nolint:gosec // G304: path comes from operator-controlled config, not user input
	if err != nil {
		return nil, fmt.Errorf("failed to read CA certificate file: %w", err)
	}

	if !certPool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("no valid certificates found in %s", caCertFile)
	}

	return &tls.Config{
		RootCAs:    certPool,
		MinVersion: tls.VersionTLS12,
	}, nil
}
