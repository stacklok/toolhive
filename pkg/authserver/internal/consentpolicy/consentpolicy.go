// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package consentpolicy validates the syntax, registration eligibility predicates,
// and deployment prerequisites for administrator consent preauthorization.
package consentpolicy

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ory/fosite"

	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Validate checks the configured client IDs and the prerequisites for enabling
// consent preauthorization. An empty list preserves existing behavior.
func Validate(clientIDs []string, authorizationBase, issuer string, upstreamCount int) error {
	if len(clientIDs) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(clientIDs))
	for _, clientID := range clientIDs {
		if clientID == "" {
			return fmt.Errorf("consent_exempt_client_ids must not contain an empty ID")
		}
		if _, ok := seen[clientID]; ok {
			return fmt.Errorf("consent_exempt_client_ids contains duplicate ID %q", clientID)
		}
		seen[clientID] = struct{}{}
		if isURLShaped(clientID) {
			return fmt.Errorf("consent_exempt_client_ids must not contain URL-shaped ID %q", clientID)
		}
	}

	if upstreamCount == 0 {
		return fmt.Errorf("consent_exempt_client_ids requires at least one configured upstream")
	}

	if authorizationBase == "" {
		authorizationBase = issuer
	}
	parsedBase, err := url.Parse(authorizationBase)
	if err != nil || !strings.EqualFold(parsedBase.Scheme, "https") || parsedBase.Host == "" {
		return fmt.Errorf("consent_exempt_client_ids requires an HTTPS authorization endpoint base URL")
	}
	return nil
}

// IsBackChannelOnlyClient reports whether client has no interactive /authorize
// flow, using the same explicit marker and metadata-shape exclusions at startup
// and runtime. Any client without the authorization_code grant qualifies, so
// /authorize answers it exactly like an unknown client ID instead of letting
// fosite's redirect_uri validation reveal that the ID exists.
func IsBackChannelOnlyClient(client fosite.Client) bool {
	return registration.BackChannelOnly(client) ||
		len(client.GetResponseTypes()) == 0 ||
		!client.GetGrantTypes().Has(oauthproto.GrantTypeAuthorizationCode)
}

// IsEligibleClient reports whether client supports ToolHive's interactive
// authorization-code flow and can be preauthorized to skip its consent page.
func IsEligibleClient(client fosite.Client) bool {
	return client != nil &&
		!client.IsPublic() &&
		!IsBackChannelOnlyClient(client) &&
		slices.Contains(client.GetResponseTypes(), oauthproto.ResponseTypeCode) &&
		len(client.GetRedirectURIs()) > 0
}

// isURLShaped reports whether clientID is an absolute URI, a network-path URL
// with an authority, or malformed as a URL (fail closed).
func isURLShaped(clientID string) bool {
	parsed, err := url.Parse(clientID)
	return err != nil || parsed.Scheme != "" || parsed.Host != ""
}
