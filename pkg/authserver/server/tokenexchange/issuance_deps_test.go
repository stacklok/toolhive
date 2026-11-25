// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRedemptionConstructors_SameErrors pins that both constructors refuse
// the same bad dependencies with the same error. The empty and invalid token
// endpoint checks are covered through the plain constructor in
// jwt_bearer_handler_test.go.
func TestRedemptionConstructors_SameErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*issuanceDeps)
		wantErr string
	}{
		{"nil validator", func(d *issuanceDeps) { d.validator = nil }, "JWT-bearer assertion validator must not be nil"},
		{"nil consumer", func(d *issuanceDeps) { d.consumer = nil },
			"JWT-bearer storage must implement storage.AssertionJWTConsumer"},
		{"nil config", func(d *issuanceDeps) { d.config = nil }, "JWT-bearer issuance dependencies must not be nil"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{}, nil)
			tt.mutate(&deps)

			plain, plainErr := newJWTBearerIssuanceHandler(deps)
			idJAG, idJAGErr := newIDJAGIssuanceHandler(deps, nil, nil)

			assert.EqualError(t, plainErr, tt.wantErr)
			assert.Nil(t, plain)
			assert.EqualError(t, idJAGErr, tt.wantErr)
			assert.Nil(t, idJAG)
		})
	}
}

// TestIDJAGConstructor_IssuerIsResourceWarnReadsUnfilteredIssuers pins that
// the startup warning counts every trusted issuer, not only those that accept
// id_jag.
func TestIDJAGConstructor_IssuerIsResourceWarnReadsUnfilteredIssuers(t *testing.T) {
	t.Parallel()

	const wantWarn = `level=WARN msg="The issuer identifier is also a resource audience (allowed_audiences), ` +
		`so ID-JAGs with aud set to the issuer are not accepted by default; ` +
		`have the IdP use the token endpoint as aud, or stop listing the issuer in allowed_audiences" ` +
		`issuer=` + idJAGTestASIssuer
	tests := []struct {
		name     string
		audience []string
		types    []JWTBearerAssertionType
		wantWarn bool
	}{
		{"jwt_bearer-only issuer without accepted audiences still counts", nil,
			[]JWTBearerAssertionType{JWTBearerAssertionTypeJWTBearer}, true},
		{"jwt_bearer-only issuer with accepted audiences does not", []string{idJAGTestOtherAud},
			[]JWTBearerAssertionType{JWTBearerAssertionTypeJWTBearer}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{
				testRedemptionIssuer(idJAGTestIssuer, tt.audience, tt.types...),
			})
			require.NoError(t, err)
			deps := testIssuanceDeps(&testJWTBearerAssertionValidator{}, resolved)
			deps.config.AccessTokenIssuer = idJAGTestASIssuer
			deps.allowedAudiences = []string{idJAGTestASIssuer, idJAGTestResource}
			var buf strings.Builder
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
				ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
					if len(groups) == 0 && a.Key == slog.TimeKey {
						return slog.Attr{}
					}
					return a
				},
			}))

			_, err = newIDJAGIssuanceHandler(deps, nil, logger)
			require.NoError(t, err)

			warns := logLinesAt(&buf, "WARN")
			if tt.wantWarn {
				assert.Equal(t, []string{wantWarn}, warns)
				return
			}
			assert.Empty(t, warns)
		})
	}
}
