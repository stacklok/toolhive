// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"errors"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"

	"github.com/stacklok/toolhive/pkg/authserver/storage"
)

// issuanceDeps holds the compose-time inputs both JWT-bearer redemption
// handlers are built from.
type issuanceDeps struct {
	validator        JWTBearerAssertionValidator
	tokenEndpoint    string
	consumer         storage.AssertionJWTConsumer
	config           *fosite.Config
	strategy         oauth2.AccessTokenStrategy
	tokenStorage     oauth2.AccessTokenStorage
	trustedIssuers   []TrustedIssuer // resolved, NOT filtered by assertion type
	allowedAudiences []string        // only the ID-JAG handler reads it
}

// validate checks every dependency, in this order: validator, token
// endpoint, consumer, then the fosite dependencies. A new field needs its
// check here.
func (d issuanceDeps) validate() error {
	if err := checkValidatorAndEndpoint(d.validator, d.tokenEndpoint); err != nil {
		return err
	}
	if d.consumer == nil {
		return errors.New("JWT-bearer storage must implement storage.AssertionJWTConsumer")
	}
	if d.config == nil || d.strategy == nil || d.tokenStorage == nil {
		return errors.New("JWT-bearer issuance dependencies must not be nil")
	}
	return nil
}

// policiesFor builds the per-issuer policy map for one assertion form. An
// empty map is valid.
func (d issuanceDeps) policiesFor(form JWTBearerAssertionType) map[string]*JWTBearerGrantPolicy {
	policies := make(map[string]*JWTBearerGrantPolicy)
	for _, issuer := range IssuersAcceptingAssertionType(d.trustedIssuers, form) {
		if issuer.JWTBearerGrant != nil {
			policies[issuer.IssuerURL] = issuer.JWTBearerGrant
		}
	}
	return policies
}
