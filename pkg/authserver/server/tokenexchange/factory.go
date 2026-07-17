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
// Returns an error if delegationLifespan is not in (0, server.MaxAccessTokenLifespan]: a zero
// or negative value would produce delegated tokens with an expiry already in the past, and a
// value above the access token ceiling would only be caught at request time by the per-request cap.
func Factory(delegationLifespan time.Duration) (server.Factory, error) {
	if delegationLifespan <= 0 || delegationLifespan > server.MaxAccessTokenLifespan {
		return nil, fmt.Errorf("tokenexchange: delegationLifespan must be between %v and %v, got %v",
			time.Duration(0), server.MaxAccessTokenLifespan, delegationLifespan)
	}
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		validator, err := NewSubjectTokenValidator(config.PublicJWKS(), config.GetAccessTokenIssuer(), config.AllowedAudiences)
		if err != nil {
			return nil, fmt.Errorf("tokenexchange: failed to create subject token validator: %w", err)
		}

		// Use the embedded *fosite.Config for HandleHelper and handlerConfig
		// because AuthorizationServerConfig shadows GetAccessTokenLifespan() without
		// a context parameter, which doesn't satisfy fosite's provider interfaces.
		atStrategy, ok := strategy.(oauth2.AccessTokenStrategy)
		if !ok {
			return nil, fmt.Errorf("tokenexchange: strategy does not implement oauth2.AccessTokenStrategy (got %T)", strategy)
		}
		atStorage, ok := storage.(oauth2.AccessTokenStorage)
		if !ok {
			return nil, fmt.Errorf("tokenexchange: storage does not implement oauth2.AccessTokenStorage (got %T)", storage)
		}
		return &Handler{
			HandleHelper: &oauth2.HandleHelper{
				AccessTokenStrategy: atStrategy,
				AccessTokenStorage:  atStorage,
				Config:              config.Config,
			},
			validator:          validator,
			delegationLifespan: delegationLifespan,
			config:             config.Config,
			allowedAudiences:   config.AllowedAudiences,
		}, nil
	}, nil
}
