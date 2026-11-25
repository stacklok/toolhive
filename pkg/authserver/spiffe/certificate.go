// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

import (
	"crypto/x509"
	"fmt"

	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/x509svid"
)

// SPIFFEIDFromCertificate returns the single SPIFFE URI SAN in cert. It rejects
// the root SPIFFE ID (empty path); go-spiffe's parser already enforces a
// canonical path.
func SPIFFEIDFromCertificate(cert *x509.Certificate) (spiffeid.ID, error) {
	id, err := x509svid.IDFromCert(cert)
	if err != nil {
		return spiffeid.ID{}, err
	}
	if id.Path() == "" {
		return spiffeid.ID{}, fmt.Errorf("SPIFFE ID path is invalid")
	}
	return id, nil
}
