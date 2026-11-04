// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	forbiddenResourceURI = "harness://static/secret"
	publicResourceURI    = "harness://static/public"
)

// rejectedResourceURIs are respellings of forbiddenResourceURI that a URL
// parser resolves to it, or other URIs not in canonical form, plus control
// bytes. An exact comparison never matches the first group.
var rejectedResourceURIs = []string{
	" " + forbiddenResourceURI,
	forbiddenResourceURI + " ",
	forbiddenResourceURI + "\t",
	forbiddenResourceURI + "\x7f",
	"\x00" + forbiddenResourceURI,
	"harness://static/se\tcret",
	"harness://static/se\ncret",
	"harness://static/a b",
	"HARNESS://static/secret",
	"Harness://static/secret",
	"harness://static/./secret",
	"harness://static/x/../secret",
	"harness://static/%2e%2e/secret",
	"harness://static/%73ecret",
	"harness://@static/secret",
	"harness://:@static/secret",
	"harness://static:/secret",
	"harness://static/secret%3a",
	`harness://static\secret`,
	"harness://static/secret#fragment",
	"secret",
}

// concreteURIMethods name a concrete resource URI, so the canonical-form check
// applies to them.
var concreteURIMethods = []string{
	"resources/read", "resources/subscribe", "resources/unsubscribe", "subscriptions/listen",
}

func uriName(uri string) string { return fmt.Sprintf("%q", uri) }

func rpcRequest(method string, params map[string]any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
}

// resourceURIRequests build the request envelope for each method that names a
// single resource URI. subscriptions/listen uses listenBody.
var resourceURIRequests = map[string]func(uri string) map[string]any{
	"resources/read": func(uri string) map[string]any {
		return rpcRequest("resources/read", map[string]any{"uri": uri})
	},
	"resources/subscribe": func(uri string) map[string]any {
		return rpcRequest("resources/subscribe", map[string]any{"uri": uri})
	},
	"resources/unsubscribe": func(uri string) map[string]any {
		return rpcRequest("resources/unsubscribe", map[string]any{"uri": uri})
	},
	"completion/complete": func(uri string) map[string]any {
		return rpcRequest("completion/complete", map[string]any{
			"ref":      map[string]any{"type": "ref/resource", "uri": uri},
			"argument": map[string]any{"name": "a", "value": "b"},
		})
	},
}

// resourceURIBody builds the request body for method naming uri. It uses
// json.Marshal so control characters are escaped the way a client sends them.
func resourceURIBody(t *testing.T, method, uri string) string {
	t.Helper()
	if method == "subscriptions/listen" {
		return listenBody(uri)
	}
	b, err := json.Marshal(resourceURIRequests[method](uri))
	require.NoError(t, err)
	return string(b)
}

// allowExceptForbidden is the deny-list policy "permit everything, forbid
// forbiddenResourceURI" evaluated on the exact string.
func allowExceptForbidden(id string) bool { return id != forbiddenResourceURI }

func TestConcreteResourceURINotInCanonicalFormIsDeniedBeforeAuthorization(t *testing.T) {
	t.Parallel()

	for _, method := range concreteURIMethods {
		for _, uri := range rejectedResourceURIs {
			t.Run(method+"/"+uriName(uri), func(t *testing.T) {
				t.Parallel()

				got := runDerived(t, resourceURIBody(t, method, uri), allowExceptForbidden)

				assert.Equal(t, http.StatusForbidden, got.status)
				assert.False(t, got.backendCalled, "backend must not be reached")
				assert.Empty(t, got.checks, "authorizer must not be consulted")
			})
		}
	}
}

func TestSubscriptionsListenDeniesNonCanonicalURIAmongValidOnes(t *testing.T) {
	t.Parallel()

	for _, uri := range rejectedResourceURIs {
		t.Run(uriName(uri), func(t *testing.T) {
			t.Parallel()

			got := runDerived(t, listenBody(publicResourceURI, uri, "harness://static/other"), allowAll)

			assert.Equal(t, http.StatusForbidden, got.status)
			assert.False(t, got.backendCalled)
			assert.Empty(t, got.checks, "the whole request is refused while checks are derived")
		})
	}
}

func TestResourceURIDecidedByPolicyWhenCanonical(t *testing.T) {
	t.Parallel()

	for _, method := range slices.Concat(concreteURIMethods, []string{"completion/complete"}) {
		t.Run(method+"/exact forbidden URI is denied by policy", func(t *testing.T) {
			t.Parallel()

			got := runDerived(t, resourceURIBody(t, method, forbiddenResourceURI), allowExceptForbidden)

			assert.Equal(t, http.StatusForbidden, got.status)
			assert.False(t, got.backendCalled)
			require.Len(t, got.checks, 1)
			assert.Equal(t, forbiddenResourceURI, got.checks[0].resourceID)
		})
		t.Run(method+"/public URI reaches the backend", func(t *testing.T) {
			t.Parallel()

			got := runDerived(t, resourceURIBody(t, method, publicResourceURI), allowExceptForbidden)

			assert.Equal(t, http.StatusOK, got.status)
			assert.True(t, got.backendCalled)
			require.Len(t, got.checks, 1)
			assert.Equal(t, publicResourceURI, got.checks[0].resourceID)
		})
	}
}

// A completion ref is a template string that SDKs match by exact string, so it
// is authorized as sent; only spaces and control characters are refused.
func TestCompletionResourceRefIsAuthorizedAsSent(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"HARNESS://static/secret", "harness://static/./secret", "file:///{name}", "harness://{+path}",
	} {
		t.Run(uriName(uri), func(t *testing.T) {
			t.Parallel()

			got := runDerived(t, resourceURIBody(t, "completion/complete", uri), allowExceptForbidden)

			assert.Equal(t, http.StatusOK, got.status)
			assert.True(t, got.backendCalled)
			require.Len(t, got.checks, 1)
			assert.Equal(t, uri, got.checks[0].resourceID)
		})
	}

	for _, uri := range []string{" harness://static/secret", "harness://static/a b", "harness://static/a\tb", "\x00x"} {
		t.Run("rejects "+uriName(uri), func(t *testing.T) {
			t.Parallel()

			got := runDerived(t, resourceURIBody(t, "completion/complete", uri), allowAll)

			assert.Equal(t, http.StatusForbidden, got.status)
			assert.False(t, got.backendCalled)
			assert.Empty(t, got.checks)
		})
	}
}

// A JSON escape such as \u0009 is decoded before the check, so it is denied
// like a raw tab in the URI.
func TestResourceURIEscapedControlCharacterIsDenied(t *testing.T) {
	t.Parallel()
	body := `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"harness://static/se\u0009cret"}}`

	got := runDerived(t, body, allowAll)

	assert.Equal(t, http.StatusForbidden, got.status)
	assert.False(t, got.backendCalled)
	assert.Empty(t, got.checks)
}
