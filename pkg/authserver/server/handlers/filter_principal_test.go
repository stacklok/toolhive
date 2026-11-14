// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilterPrincipalAccessors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		principal       filterPrincipal
		wantSubject     string
		wantCanonicalID string
		wantName        string
		wantEmail       string
		wantClaims      map[string]any
	}{
		{
			name: "upstream subject differs from canonical user ID",
			principal: filterPrincipal{
				subject:         "provider-subject",
				canonicalUserID: "canonical-user",
				name:            "Ada Lovelace",
				email:           "ada@example.com",
				claims:          map[string]any{"groups": []any{"admins"}},
			},
			wantSubject:     "provider-subject",
			wantCanonicalID: "canonical-user",
			wantName:        "Ada Lovelace",
			wantEmail:       "ada@example.com",
			wantClaims:      map[string]any{"groups": []any{"admins"}},
		},
		{
			name: "nil claims remain nil",
			principal: filterPrincipal{
				subject:         "subject",
				canonicalUserID: "user",
			},
			wantSubject:     "subject",
			wantCanonicalID: "user",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var principal FilterPrincipal = tt.principal
			assert.Equal(t, tt.wantSubject, principal.GetSubject())
			assert.Equal(t, tt.wantCanonicalID, principal.GetCanonicalUserID())
			assert.Equal(t, tt.wantName, principal.GetName())
			assert.Equal(t, tt.wantEmail, principal.GetEmail())
			assert.Equal(t, tt.wantClaims, principal.GetClaims())
		})
	}
}
