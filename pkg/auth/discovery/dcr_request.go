// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"fmt"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// NewDynamicClientRegistrationRequest constructs a DCR request for the CLI OAuth flow.
//
// The redirect URI is always http://localhost:<callbackPort>/callback, following
// RFC 8252 Section 7.3 which specifies loopback interface redirects for native
// public clients. This loopback assumption is specific to the CLI flow and must
// not be moved into the protocol package.
func NewDynamicClientRegistrationRequest(scopes []string, callbackPort int) *oauthproto.DynamicClientRegistrationRequest {
	redirectURIs := []string{fmt.Sprintf("http://localhost:%d/callback", callbackPort)}

	return &oauthproto.DynamicClientRegistrationRequest{
		ClientName:              oauthproto.ToolHiveMCPClientName,
		RedirectURIs:            redirectURIs,
		TokenEndpointAuthMethod: oauthproto.TokenEndpointAuthMethodNone, // For PKCE flow
		GrantTypes:              []string{oauthproto.GrantTypeAuthorizationCode, oauthproto.GrantTypeRefreshToken},
		ResponseTypes:           []string{oauthproto.ResponseTypeCode},
		Scopes:                  scopes,
	}
}
