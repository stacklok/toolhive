// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"fmt"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/server"
)

// FactoryConfig holds configuration for the token exchange handler factory.
type FactoryConfig struct {
	// DelegationLifespan is the maximum lifetime for delegated tokens.
	DelegationLifespan time.Duration
	// TrustedIssuers configures external OIDC issuers whose tokens are accepted
	// as subject tokens during token exchange. When empty, only self-issued tokens
	// are accepted.
	TrustedIssuers []TrustedIssuer
}

// Factory returns a server.Factory that creates a token exchange Handler.
// The cfg parameter configures the delegation token lifetime and optional
// trusted external issuers for multi-issuer token exchange.
func Factory(cfg FactoryConfig) server.Factory {
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) any {
		selfValidator, err := NewSelfIssuedTokenValidator(config.SigningJWKS, config.GetAccessTokenIssuer())
		if err != nil {
			// This is a programming error — the config should always have a valid JWKS and issuer.
			panic(fmt.Sprintf("tokenexchange: failed to create self-issued validator: %v", err))
		}

		// Build the subject token validator: multi-issuer if trusted issuers are
		// configured, otherwise self-issued only (backward compatible).
		var validator SubjectTokenValidator
		if len(cfg.TrustedIssuers) > 0 {
			validator = NewMultiIssuerTokenValidator(selfValidator, config.GetAccessTokenIssuer(), cfg.TrustedIssuers)
		} else {
			validator = selfValidator
		}

		// Use the embedded *fosite.Config for HandleHelper and handlerConfig
		// because AuthorizationServerConfig shadows GetAccessTokenLifespan() without
		// a context parameter, which doesn't satisfy fosite's provider interfaces.
		return &Handler{
			HandleHelper: &oauth2.HandleHelper{
				AccessTokenStrategy: strategy.(oauth2.AccessTokenStrategy),
				AccessTokenStorage:  storage.(oauth2.AccessTokenStorage),
				Config:              config.Config,
			},
			validator:          validator,
			selfValidator:      selfValidator,
			delegationLifespan: cfg.DelegationLifespan,
			config:             config.Config,
		}
	}
}
