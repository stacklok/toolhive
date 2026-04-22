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

// Factory returns a server.Factory that creates a token exchange Handler.
// The delegationLifespan parameter sets the maximum lifetime for delegated tokens;
// the actual lifetime is the minimum of this value and the subject token's remaining lifetime.
func Factory(delegationLifespan time.Duration) server.Factory {
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) any {
		validator, err := NewSubjectTokenValidator(config.SigningJWKS, config.GetAccessTokenIssuer())
		if err != nil {
			// This is a programming error — the config should always have a valid JWKS and issuer.
			panic(fmt.Sprintf("tokenexchange: failed to create subject token validator: %v", err))
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
			delegationLifespan: delegationLifespan,
			config:             config.Config,
		}
	}
}
