// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authserver

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func TestIntegration_JWTBearerRedemptionRefusesConfirmationClaim(t *testing.T) {
	t.Parallel()

	ts, signAssertion := setupJWTBearerGrantTestServer(t, withoutUpstreams())
	now := time.Now()
	redeem := func(t *testing.T, assertion string) (int, map[string]any) {
		t.Helper()
		return redeemIDJAG(t, ts.Server.URL, url.Values{
			"grant_type": {oauthproto.GrantTypeJWTBearer},
			"assertion":  {assertion},
			"resource":   {testAudience},
		})
	}

	for name, cnf := range map[string]any{
		"cnf null":   nil,
		"cnf object": map[string]any{"jkt": "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertion := ts.signAssertionWith(map[string]any{"cnf": cnf},
				"external-subject", now, now.Add(2*time.Minute), "plain-"+strings.ReplaceAll(name, " ", "-"))

			status, body := redeem(t, assertion)

			assert.Equal(t, http.StatusBadRequest, status, body)
			assert.Equal(t, "invalid_grant", body["error"])
			assert.Equal(t, "invalid_assertion: The JWT bearer assertion must not carry a 'cnf' claim.", body["error_description"])
		})
	}

	t.Run("an assertion without cnf still redeems", func(t *testing.T) {
		t.Parallel()
		status, body := redeem(t, signAssertion("external-subject", now, now.Add(2*time.Minute), "plain-no-cnf"))

		assert.Equal(t, http.StatusOK, status, body)
	})
}
