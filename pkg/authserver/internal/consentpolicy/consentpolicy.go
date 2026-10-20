// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package consentpolicy validates the syntax and deployment prerequisites for
// administrator consent preauthorization.
package consentpolicy

import (
	"fmt"
	"net/url"
	"strings"
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

// isURLShaped reports whether clientID is an absolute URI or a network-path URL
// with an authority.
func isURLShaped(clientID string) bool {
	parsed, err := url.Parse(clientID)
	return err == nil && (parsed.Scheme != "" || parsed.Host != "")
}
