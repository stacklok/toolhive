// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/require"
)

// testRedemptionIssuer returns a trusted issuer with one subject binding and
// the given accepted audiences and assertion types (none means unset).
func testRedemptionIssuer(issuerURL string, acceptedAudiences []string, types ...JWTBearerAssertionType) TrustedIssuer {
	return TrustedIssuer{
		IssuerURL:              issuerURL,
		ExpectedAudience:       idJAGTestResource,
		AllowedDelegateClients: []string{anyDelegateClient},
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge:   time.Hour.String(),
			AcceptedAudiences: acceptedAudiences,
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: idJAGTestSubject, AllowedResources: []string{idJAGTestResource}},
			},
			AcceptedAssertionTypes: types,
		},
	}
}

// testIssuanceDeps returns dependencies a redemption handler builds from.
// Tests set fields to override a default.
func testIssuanceDeps(validator JWTBearerAssertionValidator, trustedIssuers []TrustedIssuer) issuanceDeps {
	return issuanceDeps{
		validator:     validator,
		tokenEndpoint: testTokenEndpoint,
		consumer:      &recordingAssertionConsumer{},
		config: &fosite.Config{
			AccessTokenLifespan:      time.Hour,
			AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy,
		},
		strategy:       &mockAccessTokenStrategy{},
		tokenStorage:   &mockAccessTokenStorage{},
		trustedIssuers: trustedIssuers,
	}
}

func newTestJWTBearerHandler(t *testing.T, deps issuanceDeps) *JWTBearerHandler {
	t.Helper()
	handler, err := newJWTBearerIssuanceHandler(deps)
	require.NoError(t, err)
	return handler
}

func newTestIDJAGHandler(t *testing.T, deps issuanceDeps) *IDJAGHandler {
	t.Helper()
	handler, err := newIDJAGIssuanceHandler(deps, nil, nil)
	require.NoError(t, err)
	return handler
}
