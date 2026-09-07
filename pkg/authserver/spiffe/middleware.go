// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import (
	"crypto/x509"
	"fmt"
	"net/http"
	"path"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

// Middleware extracts a claimed SPIFFE ID from token-endpoint client certificates that contain a SPIFFE URI SAN.
// Certificates without a SPIFFE URI SAN pass through unchanged; credential validation remains the responsibility of
// the client-authentication strategy.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" || r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		cert := r.TLS.PeerCertificates[0]
		if !hasSPIFFEURI(cert) {
			next.ServeHTTP(w, r)
			return
		}

		id, err := SPIFFEIDFromCertificate(cert)
		if err != nil {
			http.Error(w, "invalid client", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(ContextWithSPIFFEID(r.Context(), id)))
	})
}

// SPIFFEIDFromCertificate returns the single SPIFFE URI SAN in cert if it has a non-root, canonical path.
func SPIFFEIDFromCertificate(cert *x509.Certificate) (spiffeid.ID, error) {
	id, err := x509svid.IDFromCert(cert)
	if err != nil {
		return spiffeid.ID{}, err
	}
	if id.Path() == "" || path.Clean(id.Path()) != id.Path() {
		return spiffeid.ID{}, fmt.Errorf("SPIFFE ID path is invalid")
	}
	return id, nil
}

func hasSPIFFEURI(cert *x509.Certificate) bool {
	for _, uri := range cert.URIs {
		if uri != nil && uri.Scheme == "spiffe" {
			return true
		}
	}
	return false
}
