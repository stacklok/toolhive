// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import (
	"crypto/x509"
	"fmt"
	"net/http"
	"path"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
)

// Middleware extracts the claimed SPIFFE ID from a token-endpoint client certificate.
// Credential validation remains the responsibility of the client-authentication strategy.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		id, err := SPIFFEIDFromCertificate(r.TLS.PeerCertificates[0])
		if err != nil {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithSPIFFEID(r.Context(), id)))
	})
}

// SPIFFEIDFromCertificate returns the single non-root SPIFFE URI SAN in cert.
// Non-SPIFFE URI SANs are intentionally ignored for cert-manager compatibility.
func SPIFFEIDFromCertificate(cert *x509.Certificate) (spiffeid.ID, error) {
	var id spiffeid.ID
	for _, uri := range cert.URIs {
		if uri.Scheme != "spiffe" {
			continue
		}
		parsed, err := spiffeid.FromURI(uri)
		if err != nil {
			return spiffeid.ID{}, err
		}
		if parsed.Path() == "" || path.Clean(parsed.Path()) != parsed.Path() {
			return spiffeid.ID{}, fmt.Errorf("SPIFFE ID path is invalid")
		}
		if id != (spiffeid.ID{}) {
			return spiffeid.ID{}, fmt.Errorf("multiple SPIFFE URI SANs")
		}
		id = parsed
	}
	if id == (spiffeid.ID{}) {
		return spiffeid.ID{}, fmt.Errorf("SPIFFE URI SAN is required")
	}
	return id, nil
}
