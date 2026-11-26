// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package obo

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/audit"
	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/obo/entra"
)

func TestNewInteractionRequiredAuditEvent(t *testing.T) {
	t.Parallel()

	const component = "test-component"
	source := audit.EventSource{Type: audit.SourceTypeNetwork, Value: "203.0.113.7"}
	exErr := &entra.ExchangeError{
		Kind:        entra.KindInteractionRequired,
		OAuthError:  "interaction_required",
		Description: "AADSTS50079: proof-up required",
		Claims:      "CLAIMS-BLOB",
		HTTPStatus:  http.StatusBadRequest,
	}
	entraExtra := map[string]any{
		"reason":                   "interaction_required",
		"oauth_error":              "interaction_required",
		"description":              "AADSTS50079: proof-up required",
		"http_status":              http.StatusBadRequest,
		"claims_challenge_present": true,
	}

	tests := []struct {
		name         string
		identity     *auth.Identity
		err          error
		wantSubjects map[string]string
		wantExtra    map[string]any
	}{
		{
			name:         "entra error with identity",
			identity:     &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123"}},
			err:          exErr,
			wantSubjects: map[string]string{"sub": "user-123"},
			wantExtra:    entraExtra,
		},
		{
			name:      "wrapped entra error is still unwrapped",
			identity:  &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123"}},
			err:       fmt.Errorf("strategy: %w", exErr),
			wantExtra: entraExtra,
			wantSubjects: map[string]string{
				"sub": "user-123",
			},
		},
		{
			name:      "nil identity records no subject",
			identity:  nil,
			err:       exErr,
			wantExtra: entraExtra,
		},
		{
			name:      "identity without a subject records no subject",
			identity:  &auth.Identity{Token: "USER-TOKEN"},
			err:       exErr,
			wantExtra: entraExtra,
		},
		{
			name:         "non-entra error records only the reason",
			identity:     &auth.Identity{PrincipalInfo: auth.PrincipalInfo{Subject: "user-123"}},
			err:          errors.New("some other failure"),
			wantSubjects: map[string]string{"sub": "user-123"},
			wantExtra:    map[string]any{"reason": "interaction_required"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event := NewInteractionRequiredAuditEvent(source, component, tt.identity, testTarget, tt.err)

			assert.Equal(t, AuditEventType, event.Type)
			assert.Equal(t, audit.OutcomeDenied, event.Outcome)
			assert.Equal(t, component, event.Component)
			assert.Equal(t, source, event.Source)
			assert.Equal(t, map[string]string{"audience": testTarget}, event.Target)
			if tt.wantSubjects == nil {
				assert.Empty(t, event.Subjects)
			} else {
				assert.Equal(t, tt.wantSubjects, event.Subjects)
			}
			assert.Equal(t, tt.wantExtra, event.Metadata.Extra)
		})
	}
}
