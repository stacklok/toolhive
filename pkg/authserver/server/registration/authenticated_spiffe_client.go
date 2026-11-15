// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package registration

import (
	"fmt"

	"github.com/ory/fosite"
	"github.com/spiffe/go-spiffe/v2/spiffeid"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

// staticSPIFFEClient lets AuthenticatedSPIFFEClient embed *SPIFFEClient under an
// unexported field name, so another package cannot set its fields and must use
// NewAuthenticatedSPIFFEClient. The zero value is not valid.
type staticSPIFFEClient = SPIFFEClient

// AuthenticatedSPIFFEClient is a static SPIFFE client together with the SPIFFE
// ID and method of the workload credential that was verified. Only the
// client-authentication resolver builds one in production: its presence on a
// request is what tells grant handlers a workload credential was verified.
type AuthenticatedSPIFFEClient struct {
	*staticSPIFFEClient
	spiffeID spiffeid.ID
	method   spiffeauth.SPIFFEAuthenticationMethod
}

// NewAuthenticatedSPIFFEClient binds a static SPIFFE client to the verified
// SPIFFE ID and authentication method of the workload.
func NewAuthenticatedSPIFFEClient(
	client *SPIFFEClient, id spiffeid.ID, method spiffeauth.SPIFFEAuthenticationMethod,
) (*AuthenticatedSPIFFEClient, error) {
	if client == nil {
		return nil, fmt.Errorf("SPIFFE client is required")
	}
	if id.IsZero() {
		return nil, fmt.Errorf("SPIFFE ID is required")
	}
	if method != spiffeauth.SPIFFEAuthenticationMethodX509 && method != spiffeauth.SPIFFEAuthenticationMethodJWT {
		return nil, fmt.Errorf("unknown SPIFFE authentication method %q", method)
	}
	return &AuthenticatedSPIFFEClient{staticSPIFFEClient: client, spiffeID: id, method: method}, nil
}

// SPIFFEID returns the verified SPIFFE ID of the workload.
func (c *AuthenticatedSPIFFEClient) SPIFFEID() spiffeid.ID { return c.spiffeID }

// AuthenticationMethod returns the credential type that was verified.
func (c *AuthenticatedSPIFFEClient) AuthenticationMethod() spiffeauth.SPIFFEAuthenticationMethod {
	return c.method
}

var _ fosite.Client = (*AuthenticatedSPIFFEClient)(nil)
