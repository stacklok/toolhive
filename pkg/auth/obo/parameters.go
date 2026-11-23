// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"fmt"
	"strings"

	"github.com/stacklok/toolhive/pkg/auth/obo/exchange"
)

// ClientSecretEnvVar is the environment-variable name the operator injects the
// OBO confidential-client secret into for a proxyrunner pod (MCPServer and
// MCPRemoteProxy). A proxy carries at most one OBO config, so a fixed name is
// enough. The vMCP pod can carry several OBO backends and therefore uses a
// per-config name instead (see controllerutil.GenerateUniqueOBOSecretEnvVarName).
//
//nolint:gosec // G101: this is an environment-variable name, not a credential.
const ClientSecretEnvVar = "TOOLHIVE_OBO_CLIENT_SECRET"

// MiddlewareParameters is the operator-to-runtime contract carried in the OBO
// middleware's MiddlewareConfig.Parameters. The operator marshals it and the
// proxy middleware factory unmarshals it, so the JSON key set is defined once.
//
// It deliberately uses only standard-library types so this package stays free
// of the Kubernetes dependencies the proxyrunner must not link.
type MiddlewareParameters struct {
	// TokenURL is the fully-formed Entra tenant token endpoint, e.g.
	// https://login.microsoftonline.com/<tenant>/oauth2/v2.0/token.
	//
	// SECURITY: TokenURL is an operator trust boundary. The OBO exchange POSTs
	// the client secret and the end-user assertion in the request body to this
	// URL, so the operator is responsible for pointing it only at a trusted
	// Entra token endpoint. See entra.NewFromTokenURL.
	TokenURL string `json:"tokenUrl"`

	// ClientID is the confidential client's application (client) ID.
	ClientID string `json:"clientId"`

	// Audience is the backend resource identifier (Application ID URI or client
	// ID). Used as the exchange scope <audience>/.default when Scopes is empty
	// (see ExchangeTarget).
	Audience string `json:"audience"`

	// Scopes are the delegated scopes to request, e.g.
	// ["api://<backend>/.default"]. When present they take precedence over
	// Audience (see ExchangeTarget).
	Scopes []string `json:"scopes"`

	// ClientSecretEnvVar is the name of the environment variable holding the
	// client secret. The operator injects the value into the proxyrunner pod;
	// only the name travels in the contract.
	ClientSecretEnvVar string `json:"clientSecretEnvVar"`

	// SubjectTokenProviderName selects the source of the OBO subject (assertion)
	// token from the request's authenticated Identity:
	//
	//   - Empty (default): use the inbound token the client presented
	//     (Identity.Token). This is the deployment with no embedded auth server,
	//     where the client holds an Entra token directly.
	//   - Set: use the named upstream provider's token
	//     (Identity.UpstreamTokens[SubjectTokenProviderName]). Set this when an
	//     embedded auth server fronts the proxy, so the inbound token is the
	//     proxy's own session token rather than an Entra token. Mirrors the
	//     upstreamswap middleware.
	//
	// Either way the subject comes from the Identity, so the authentication
	// middleware must run first.
	SubjectTokenProviderName string `json:"subjectTokenProviderName,omitempty"`

	// CacheSkewSeconds overrides the token cache's default expiry skew. Zero
	// leaves the cache default; negative is rejected by Validate.
	CacheSkewSeconds int `json:"cacheSkewSeconds"`
}

// ExchangeTarget returns the single value the Exchanger receives as its
// audience argument; see exchange.Target for the collapse rule.
func (p *MiddlewareParameters) ExchangeTarget() string {
	return exchange.Target(p.Audience, p.Scopes)
}

// Validate checks the contract's required fields and bounds, failing loudly so
// a misconfiguration surfaces when the parameters are first consumed rather
// than per request. It does not validate TokenURL's URL shape; that is entra's
// concern (see entra.NewFromTokenURL).
func (p *MiddlewareParameters) Validate() error {
	if strings.TrimSpace(p.TokenURL) == "" {
		return fmt.Errorf("obo parameters: tokenUrl is required")
	}
	if strings.TrimSpace(p.ClientID) == "" {
		return fmt.Errorf("obo parameters: clientId is required")
	}
	if strings.TrimSpace(p.ClientSecretEnvVar) == "" {
		return fmt.Errorf("obo parameters: clientSecretEnvVar is required")
	}
	if p.ExchangeTarget() == "" {
		return fmt.Errorf("obo parameters: one of scopes or audience is required")
	}
	if p.CacheSkewSeconds < 0 {
		return fmt.Errorf("obo parameters: cacheSkewSeconds must not be negative")
	}
	return nil
}
