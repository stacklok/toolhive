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
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/oauthproto"
)

func idJAGRedemptionForm(assertion string, clientCreds url.Values) url.Values {
	form := url.Values{
		"grant_type": {oauthproto.GrantTypeJWTBearer},
		"assertion":  {assertion},
		"resource":   {testAudience},
	}
	for k, v := range clientCreds {
		form[k] = v
	}
	return form
}

func redeemIDJAG(t *testing.T, serverURL string, form url.Values) (int, map[string]any) {
	t.Helper()
	response := makeTokenRequest(t, serverURL, form)
	t.Cleanup(func() { _ = response.Body.Close() })
	return response.StatusCode, parseTokenResponse(t, response)
}

func TestIntegration_IDJAGRedemptionRequiresClientAuthentication(t *testing.T) {
	t.Parallel()

	ts, _ := setupJWTBearerGrantTestServer(t, withoutUpstreams())
	now := time.Now()
	confidential := url.Values{"client_id": {idJAGClientID}, "client_secret": {idJAGClientSecret}}

	t.Run("a public client is refused", func(t *testing.T) {
		t.Parallel()
		// The ID-JAG names the public client, so only its public status refuses it.
		assertion := ts.signIDJAGAssertionWith(testClientID, nil, "external-subject", now, now.Add(2*time.Minute), "public-client")

		status, body := redeemIDJAG(t, ts.Server.URL,
			idJAGRedemptionForm(assertion, url.Values{"client_id": {testClientID}}))

		assert.Equal(t, http.StatusUnauthorized, status, body)
		assert.Equal(t, "invalid_client", body["error"])
		assert.True(t, strings.HasPrefix(body["error_description"].(string), "client_authentication_required"), body)
	})

	t.Run("a request with no client credentials is refused", func(t *testing.T) {
		t.Parallel()
		assertion := ts.signIDJAGAssertion("external-subject", now, now.Add(2*time.Minute), "no-credentials")

		status, body := redeemIDJAG(t, ts.Server.URL, idJAGRedemptionForm(assertion, nil))

		assert.Equal(t, http.StatusUnauthorized, status, body)
		assert.Equal(t, "invalid_client", body["error"])
		assert.True(t, strings.HasPrefix(body["error_description"].(string), "client_authentication_required"), body)
	})

	t.Run("a public client is refused before the assertion is validated", func(t *testing.T) {
		t.Parallel()
		// An ID-JAG-typed assertion with a broken signature: validating it would
		// give invalid_grant, so invalid_client shows the assertion was not judged.
		assertion := ts.signIDJAGAssertionWith(testClientID, nil, "external-subject", now, now.Add(2*time.Minute), "bad-signature") + "x"

		status, body := redeemIDJAG(t, ts.Server.URL,
			idJAGRedemptionForm(assertion, url.Values{"client_id": {testClientID}}))

		assert.Equal(t, http.StatusUnauthorized, status, body)
		assert.Equal(t, "invalid_client", body["error"])
		assert.True(t, strings.HasPrefix(body["error_description"].(string), "client_authentication_required"), body)
	})

	t.Run("a refused public client does not consume the ID-JAG", func(t *testing.T) {
		t.Parallel()
		assertion := ts.signIDJAGAssertion("external-subject", now, now.Add(2*time.Minute), "not-consumed")

		status, body := redeemIDJAG(t, ts.Server.URL,
			idJAGRedemptionForm(assertion, url.Values{"client_id": {testClientID}}))
		require.Equal(t, http.StatusUnauthorized, status, body)
		require.Equal(t, "invalid_client", body["error"])

		status, body = redeemIDJAG(t, ts.Server.URL, idJAGRedemptionForm(assertion, confidential))
		assert.Equal(t, http.StatusOK, status, body)
		assert.NotEmpty(t, body["access_token"])
	})

	t.Run("a confidential client redeems", func(t *testing.T) {
		t.Parallel()
		assertion := ts.signIDJAGAssertion("external-subject", now, now.Add(2*time.Minute), "confidential-client")

		status, body := redeemIDJAG(t, ts.Server.URL, idJAGRedemptionForm(assertion, confidential))

		assert.Equal(t, http.StatusOK, status, body)
		assert.NotEmpty(t, body["access_token"])
	})
}

func TestIntegration_IDJAGRedemptionRefusesConfirmationClaim(t *testing.T) {
	t.Parallel()

	ts, _ := setupJWTBearerGrantTestServer(t, withoutUpstreams())
	now := time.Now()
	confidential := url.Values{"client_id": {idJAGClientID}, "client_secret": {idJAGClientSecret}}

	for name, cnf := range map[string]any{
		"cnf null":   nil,
		"cnf object": map[string]any{"jkt": "0ZcOCORZNYy-DWpqq30jZyJGHTN0d2HglBV3uiguA4I"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assertion := ts.signIDJAGAssertionWith(idJAGClientID, map[string]any{"cnf": cnf},
				"external-subject", now, now.Add(2*time.Minute), "cnf-"+strings.ReplaceAll(name, " ", "-"))

			status, body := redeemIDJAG(t, ts.Server.URL, idJAGRedemptionForm(assertion, confidential))

			assert.Equal(t, http.StatusBadRequest, status, body)
			assert.Equal(t, "invalid_grant", body["error"])
			assert.Equal(t, "invalid_assertion: The ID-JAG assertion must not carry a 'cnf' claim.", body["error_description"])
		})
	}
}
