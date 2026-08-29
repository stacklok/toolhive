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

// NewSharedTrustedIssuerValidator builds the single MultiIssuerTokenValidator
// a caller should pass to both Factory and JWTBearerIssuanceFactory when
// enabling both the RFC 8693 token-exchange and RFC 7523 JWT-bearer grants
// for the same trusted issuers, so only one JWKS cache/goroutine set per
// issuer is ever registered. Returns (nil, nil) when trustedIssuers is
// empty — both factories fall back to building their own validator (or none)
// in that case.
func NewSharedTrustedIssuerValidator(
	config *server.AuthorizationServerConfig, trustedIssuers []TrustedIssuer,
) (*MultiIssuerTokenValidator, error) {
	if len(trustedIssuers) == 0 {
		return nil, nil
	}
	selfValidator, err := NewSelfIssuedTokenValidator(config.PublicJWKS(), config.GetAccessTokenIssuer(), config.AllowedAudiences)
	if err != nil {
		return nil, fmt.Errorf("failed to create self validator: %w", err)
	}
	return NewMultiIssuerTokenValidator(selfValidator, config.GetAccessTokenIssuer(), trustedIssuers, config.AllowedAudiences)
}

// Factory returns a server.Factory that creates a token exchange Handler.
// The delegationLifespan parameter sets the maximum lifetime for delegated tokens;
// the actual lifetime is the minimum of this value and the subject token's remaining lifetime.
// Returns an error if delegationLifespan is not in (0, server.MaxAccessTokenLifespan]: a zero
// or negative value would produce delegated tokens with an expiry already in the past, and a
// value above the access token ceiling would only be caught at request time by the per-request cap.
//
// When trustedIssuers is non-empty, subject tokens are validated by a
// MultiIssuerTokenValidator wrapping the self-issued validator; otherwise the
// self-issued validator is used directly, preserving prior behavior exactly.
// Each TrustedIssuer carries its own InsecureAllowHTTP/AllowPrivateIPs (see
// NewMultiIssuerTokenValidator) — this Factory takes no validator-wide
// equivalent, so a self-issuer setting can never reach the external path
// through here.
//
// configuredDelegateClients is the operator-configured list of delegate
// client IDs (Config.DelegateClients, projected down to just their
// ClientIDs by the caller). An empty list preserves existing behavior
// exactly. The trust source here is server config, not client storage: the
// set is read once at process construction, so removing a client from
// config revokes its trust on the next restart rather than requiring any
// explicit revocation step against storage.
func Factory(
	delegationLifespan time.Duration, trustedIssuers []TrustedIssuer, configuredDelegateClients []string,
) (server.Factory, error) {
	return FactoryWithSharedTrustedIssuerValidator(
		delegationLifespan, trustedIssuers, configuredDelegateClients, nil)
}

// FactoryWithSharedTrustedIssuerValidator is Factory with an optional shared
// external-issuer validator. When shared is non-nil, it is used instead of
// building a second MultiIssuerTokenValidator. Callers enabling both the RFC
// 8693 token-exchange and RFC 7523 JWT-bearer grants for the same trusted
// issuers can share the validator created by NewSharedTrustedIssuerValidator.
func FactoryWithSharedTrustedIssuerValidator(
	delegationLifespan time.Duration, trustedIssuers []TrustedIssuer, configuredDelegateClients []string,
	shared *MultiIssuerTokenValidator,
) (server.Factory, error) {
	if delegationLifespan <= 0 || delegationLifespan > server.MaxAccessTokenLifespan {
		return nil, fmt.Errorf("tokenexchange: delegationLifespan must be between %v and %v, got %v",
			time.Duration(0), server.MaxAccessTokenLifespan, delegationLifespan)
	}
	for _, id := range configuredDelegateClients {
		if id == "" {
			return nil, fmt.Errorf("tokenexchange: configuredDelegateClients must not contain an empty client ID")
		}
	}
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		selfValidator, err := NewSelfIssuedTokenValidator(config.PublicJWKS(), config.GetAccessTokenIssuer(), config.AllowedAudiences)
		if err != nil {
			return nil, fmt.Errorf("tokenexchange: failed to create subject token validator: %w", err)
		}

		// IIFE keeps validator a single immutable assignment rather than a
		// mutable var reassigned across branches (go-style): reassigning it
		// in place risked ending up with a non-nil SubjectTokenValidator
		// wrapping a nil *MultiIssuerTokenValidator on the error path.
		validator, err := func() (SubjectTokenValidator, error) {
			if shared != nil {
				return shared, nil
			}
			if len(trustedIssuers) == 0 {
				return selfValidator, nil
			}
			return NewMultiIssuerTokenValidator(selfValidator, config.GetAccessTokenIssuer(), trustedIssuers, config.AllowedAudiences)
		}()
		if err != nil {
			return nil, fmt.Errorf("tokenexchange: trusted_issuers: %w", err)
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
			validator:                 validator,
			selfValidator:             selfValidator,
			issuer:                    config.GetAccessTokenIssuer(),
			delegationLifespan:        delegationLifespan,
			config:                    config.Config,
			allowedAudiences:          config.AllowedAudiences,
			configuredDelegateClients: configuredDelegateClients,
		}, nil
	}, nil
}
