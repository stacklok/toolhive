// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package spiffeauth

const (
	// SPIFFEAuthenticationMethodX509 authenticates a workload with an X.509-SVID.
	SPIFFEAuthenticationMethodX509 SPIFFEAuthenticationMethod = "spiffe_x509"
	// SPIFFEAuthenticationMethodJWT authenticates a workload with a JWT-SVID.
	SPIFFEAuthenticationMethodJWT SPIFFEAuthenticationMethod = "spiffe_jwt"
)

// SPIFFEAuthenticationMethod identifies the credential type permitted for a
// SPIFFE workload. Methods are explicit so introducing another credential type
// cannot silently broaden a policy.
//
// It is declared here, in this leaf package, rather than as authserver's own
// type: package server's SPIFFEClientResolver (spiffe_client_auth.go) needs a
// method type for its resolver signature, and package server cannot import
// authserver (authserver imports server). authserver.SPIFFEAuthenticationMethod
// stays its own independent plain type; the two are related only by a small
// explicit conversion at the one call site that constructs the resolver
// closure (see authserver.newSPIFFEClientResolver).
type SPIFFEAuthenticationMethod string
