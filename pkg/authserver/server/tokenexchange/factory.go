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
// that backs a server's trusted-issuer handling. buildProvider constructs it
// whenever any trusted issuer is configured — not only when the RFC 7523
// JWT-bearer grant is also enabled — and passes it to
// FactoryWithSharedTrustedIssuerValidator (and, when the JWT-bearer grant is
// enabled, JWTBearerIssuanceFactory), both of which now require it for a
// non-empty trusted-issuer set rather than building their own. One shared
// instance keeps a single JWKS cache/goroutine set per issuer and, crucially,
// gives the server one validator to Close on shutdown so those goroutines are
// released (see MultiIssuerTokenValidator.Close). Returns (nil, nil) when
// trustedIssuers is empty, in which case no external validator is built.
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
// trustedIssuers must be empty here. A MultiIssuerTokenValidator owns
// per-issuer JWKS refresh worker pools that only its Close releases, and the
// bare Factory has no way to hand that instance back to the caller for
// shutdown (it is built at fosite-compose time, from config not available at
// this call). Passing a non-empty set therefore returns an error rather than
// silently building a validator whose workers leak; callers with trusted
// issuers must build it via NewSharedTrustedIssuerValidator, hold it for Close,
// and pass it to FactoryWithSharedTrustedIssuerValidator. With no trusted
// issuers the self-issued validator is used directly, preserving prior
// behavior exactly.
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

// FactoryWithSharedTrustedIssuerValidator is Factory with a shared
// external-issuer validator. shared is used as the subject-token validator when
// non-nil; it is REQUIRED whenever trustedIssuers is non-empty (an error is
// returned otherwise), because a locally-built MultiIssuerTokenValidator's JWKS
// refresh workers would have no owner to Close them — see the error below.
// Callers build it once with NewSharedTrustedIssuerValidator, hold it for Close,
// and can reuse the same instance across the RFC 8693 token-exchange and RFC
// 7523 JWT-bearer grants.
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
	// A trusted-issuer validator owns per-issuer JWKS refresh worker pools that
	// only its Close releases, but the returned closure (built at fosite-compose
	// time, from a config not available here) cannot hand that instance back to
	// the caller for shutdown. Requiring the caller to build it up front via
	// NewSharedTrustedIssuerValidator and pass it as shared is the only
	// construction path that stays releasable — fail loudly rather than silently
	// build a leaked one.
	if shared == nil && len(trustedIssuers) > 0 {
		return nil, fmt.Errorf("tokenexchange: trusted issuers require a shared validator built via " +
			"NewSharedTrustedIssuerValidator so its JWKS refresh workers can be released on shutdown")
	}
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		selfValidator, err := NewSelfIssuedTokenValidator(config.PublicJWKS(), config.GetAccessTokenIssuer(), config.AllowedAudiences)
		if err != nil {
			return nil, fmt.Errorf("tokenexchange: failed to create subject token validator: %w", err)
		}

		// shared is guaranteed non-nil whenever trustedIssuers is non-empty
		// (checked above), so the trusted-issuer path always uses the
		// caller-owned, closeable validator; this closure never constructs one
		// whose JWKS workers nothing can release. The IIFE keeps validator a
		// single immutable assignment (go-style).
		validator := func() SubjectTokenValidator {
			if shared != nil {
				return shared
			}
			return selfValidator
		}()

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
