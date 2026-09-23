// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authz

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	mcpparser "github.com/stacklok/toolhive/pkg/mcp"
)

// derivedResult captures everything a single pass through the real
// parsing+authorization chain reveals about a request: the HTTP status, whether
// the backend was reached, and every authorization decision that was requested.
type derivedResult struct {
	status        int
	backendCalled bool
	checks        []derivedCheck
}

type derivedCheck struct {
	feature    authorizers.MCPFeature
	operation  authorizers.MCPOperation
	resourceID string
	arguments  map[string]interface{}
}

// runDerived drives body through mcpparser.ParsingMiddleware + Middleware with an
// authorizer that consults allow for each decision. It is deliberately the full
// real chain rather than a unit call: the bypass these tests pin existed because
// the middleware never asked the authorizer at all, which only the wired chain
// can demonstrate.
func runDerived(t *testing.T, body string, allow func(resourceID string) bool) derivedResult {
	t.Helper()

	res := derivedResult{}
	stub := &stubAuthorizer{
		authorize: func(f authorizers.MCPFeature, o authorizers.MCPOperation, id string) (bool, error) {
			res.checks = append(res.checks, derivedCheck{feature: f, operation: o, resourceID: id})
			return allow(id), nil
		},
	}
	stub.recordArguments = func(args map[string]interface{}) {
		if n := len(res.checks); n > 0 {
			res.checks[n-1].arguments = args
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		res.backendCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
	})

	middleware := mcpparser.ParsingMiddleware(Middleware(stub, handler, nil))

	req, err := http.NewRequest(http.MethodPost, "/messages", bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.WithIdentity(req.Context(), &auth.Identity{
		PrincipalInfo: auth.PrincipalInfo{Subject: "test-user", Claims: jwt.MapClaims{"sub": "test-user"}},
	}))

	rr := httptest.NewRecorder()
	middleware.ServeHTTP(rr, req)
	res.status = rr.Code
	return res
}

func allowAll(string) bool { return true }
func denyAll(string) bool  { return false }

// TestCompletionAuthorizesReferencedCapability pins GHSA-8rgq-f53x-rcp6: a
// completion/complete request names a concrete prompt or resource template, so it
// must clear the same get/read decision prompts/get and resources/read clear.
// Before the fix the method was classified always-allowed and reached the backend
// without the authorizer being consulted once.
func TestCompletionAuthorizesReferencedCapability(t *testing.T) {
	t.Parallel()

	promptRef := `{"jsonrpc":"2.0","id":1,"method":"completion/complete","params":` +
		`{"ref":{"type":"ref/prompt","name":"restricted-deploy"},` +
		`"argument":{"name":"target","value":"prod"}}}`
	resourceRef := `{"jsonrpc":"2.0","id":1,"method":"completion/complete","params":` +
		`{"ref":{"type":"ref/resource","uri":"secrets://tenant/{name}"},` +
		`"argument":{"name":"name","value":"a"}}}`

	t.Run("denied prompt ref is rejected before dispatch", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, promptRef, denyAll)

		assert.Equal(t, http.StatusForbidden, got.status)
		assert.False(t, got.backendCalled, "denied completion must not reach the backend")
		require.Len(t, got.checks, 1, "exactly one authorization decision expected")
		assert.Equal(t, derivedCheck{
			feature:    authorizers.MCPFeaturePrompt,
			operation:  authorizers.MCPOperationGet,
			resourceID: "restricted-deploy",
		}, got.checks[0])
	})

	t.Run("allowed prompt ref reaches the backend", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, promptRef, allowAll)

		assert.Equal(t, http.StatusOK, got.status)
		assert.True(t, got.backendCalled)
		require.Len(t, got.checks, 1)
		assert.Equal(t, "restricted-deploy", got.checks[0].resourceID)
	})

	t.Run("denied resource ref is rejected before dispatch", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, resourceRef, denyAll)

		assert.Equal(t, http.StatusForbidden, got.status)
		assert.False(t, got.backendCalled)
		require.Len(t, got.checks, 1)
		assert.Equal(t, derivedCheck{
			feature:    authorizers.MCPFeatureResource,
			operation:  authorizers.MCPOperationRead,
			resourceID: "secrets://tenant/{name}",
		}, got.checks[0])
	})

	t.Run("allowed resource ref reaches the backend", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, resourceRef, allowAll)

		assert.Equal(t, http.StatusOK, got.status)
		assert.True(t, got.backendCalled)
	})
}

// TestCompletionRejectsUnauthorizableRefs pins the fail-closed half of the fix.
// A ref the middleware cannot resolve to exactly one (capability, identifier)
// pair must be denied outright rather than authorized under an empty or
// mismatched identifier, which a broad policy rule could accidentally permit.
// The authorizer must not be consulted at all: there is nothing coherent to ask.
func TestCompletionRejectsUnauthorizableRefs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params string
	}{
		{"missing ref", `{"argument":{"name":"target","value":"p"}}`},
		{"null params", `null`},
		{"ref is not an object", `{"ref":"restricted-deploy"}`},
		{"unknown ref type", `{"ref":{"type":"ref/tool","name":"weather"}}`},
		{"missing type discriminator", `{"ref":{"name":"restricted-deploy"}}`},
		{"prompt ref without name", `{"ref":{"type":"ref/prompt"}}`},
		{"prompt ref with empty name", `{"ref":{"type":"ref/prompt","name":""}}`},
		{"resource ref without uri", `{"ref":{"type":"ref/resource"}}`},
		{"resource ref with empty uri", `{"ref":{"type":"ref/resource","uri":""}}`},
		{"prompt ref with non-string name", `{"ref":{"type":"ref/prompt","name":42}}`},
		// pkg/mcp's parser falls back name->uri independently of type, so this shape
		// is exactly what could make the authorized and dispatched identifiers
		// disagree if the operation and identifier came from different branches.
		{"prompt ref carrying only a uri", `{"ref":{"type":"ref/prompt","uri":"secrets://tenant/admin"}}`},
		{"resource ref carrying only a name", `{"ref":{"type":"ref/resource","name":"restricted-deploy"}}`},
		{
			"ambiguous ref carrying both name and uri",
			`{"ref":{"type":"ref/prompt","name":"greeting","uri":"secrets://tenant/admin"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := `{"jsonrpc":"2.0","id":1,"method":"completion/complete","params":` + tt.params + `}`
			got := runDerived(t, body, allowAll)

			assert.Equal(t, http.StatusForbidden, got.status, "unauthorizable ref must be denied")
			assert.False(t, got.backendCalled, "unauthorizable ref must not reach the backend")
			assert.Empty(t, got.checks, "no authorizer call should be made for an unauthorizable ref")
		})
	}
}

// listenBody builds a subscriptions/listen request subscribing to uris.
func listenBody(uris ...string) string {
	quoted := make([]string, 0, len(uris))
	for _, u := range uris {
		quoted = append(quoted, `"`+u+`"`)
	}
	return `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
		`{"notifications":{"resourceSubscriptions":[` + strings.Join(quoted, ",") + `]}}}`
}

// TestSubscriptionsListenAuthorizesEachResource pins GHSA-r633-ppfp-jgj6: the
// Modern subscriptions/listen method carries an arbitrary list of resource URIs
// that the backend registers for change notifications, so every URI must clear
// the same read decision resources/subscribe clears. Before the fix the method
// was classified always-allowed and no URI was ever extracted.
func TestSubscriptionsListenAuthorizesEachResource(t *testing.T) {
	t.Parallel()

	t.Run("denied uri is rejected before dispatch", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, listenBody("secret://tenant-b/payroll"), denyAll)

		assert.Equal(t, http.StatusForbidden, got.status)
		assert.False(t, got.backendCalled, "denied subscription must not reach the backend")
		require.Len(t, got.checks, 1)
		assert.Equal(t, derivedCheck{
			feature:    authorizers.MCPFeatureResource,
			operation:  authorizers.MCPOperationRead,
			resourceID: "secret://tenant-b/payroll",
		}, got.checks[0])
	})

	t.Run("every allowed uri is checked and forwarded", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, listenBody("res://a", "res://b", "res://c"), allowAll)

		assert.Equal(t, http.StatusOK, got.status)
		assert.True(t, got.backendCalled)
		require.Len(t, got.checks, 3, "each requested uri must be authorized individually")
		for _, c := range got.checks {
			assert.Equal(t, authorizers.MCPFeatureResource, c.feature)
			assert.Equal(t, authorizers.MCPOperationRead, c.operation)
		}
	})

	t.Run("one denied uri denies the whole request", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, listenBody("res://allowed", "secret://denied"), func(id string) bool {
			return id != "secret://denied"
		})

		assert.Equal(t, http.StatusForbidden, got.status,
			"a mixed list must be denied as a unit, not silently filtered")
		assert.False(t, got.backendCalled,
			"the backend must never see a list containing a denied uri")
	})

	t.Run("repeated uris collapse to one decision", func(t *testing.T) {
		t.Parallel()
		// Five copies of one URI plus two others: the authorizer must be asked
		// three times, not seven. Against the HTTP PDP each avoided call is a
		// network round trip a caller would otherwise control.
		got := runDerived(t,
			listenBody("res://a", "res://b", "res://a", "res://a", "res://c", "res://a", "res://a"),
			allowAll)

		assert.Equal(t, http.StatusOK, got.status)
		assert.True(t, got.backendCalled, "the body is forwarded verbatim, duplicates included")
		require.Len(t, got.checks, 3, "duplicate uris must not each cost a decision")
		ids := []string{got.checks[0].resourceID, got.checks[1].resourceID, got.checks[2].resourceID}
		assert.Equal(t, []string{"res://a", "res://b", "res://c"}, ids,
			"first-seen order is preserved so denial attribution is deterministic")
	})

	t.Run("a denied uri repeated is still denied once", func(t *testing.T) {
		t.Parallel()
		got := runDerived(t, listenBody("res://ok", "secret://x", "secret://x"), func(id string) bool {
			return id != "secret://x"
		})

		assert.Equal(t, http.StatusForbidden, got.status)
		assert.False(t, got.backendCalled)
		assert.Len(t, got.checks, 2, "evaluation stops at the first denial")
	})

	t.Run("a list exactly at the cap is allowed", func(t *testing.T) {
		t.Parallel()
		atCap := make([]string, maxResourceSubscriptions)
		for i := range atCap {
			atCap[i] = fmt.Sprintf("res://%d", i)
		}
		got := runDerived(t, listenBody(atCap...), allowAll)

		assert.Equal(t, http.StatusOK, got.status, "the cap is inclusive")
		assert.Len(t, got.checks, maxResourceSubscriptions)
	})

	t.Run("list-changed only subscription needs no resource check", func(t *testing.T) {
		t.Parallel()
		body := `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
			`{"notifications":{"toolsListChanged":true,"resourcesListChanged":true}}}`
		got := runDerived(t, body, denyAll)

		assert.Equal(t, http.StatusOK, got.status,
			"list-changed flags carry no resource identifier and stay pass-through")
		assert.True(t, got.backendCalled)
		assert.Empty(t, got.checks)
	})
}

// TestSubscriptionsListenRejectsMalformedRequests pins the fail-closed half:
// params the middleware cannot fully decode may hide a resource URI the backend
// would still honour, so they are denied rather than forwarded unchecked.
func TestSubscriptionsListenRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, maxResourceSubscriptions+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("res://%d", i)
	}
	repeatedPastCap := make([]string, maxResourceSubscriptions+1)
	for i := range repeatedPastCap {
		repeatedPastCap[i] = "res://same"
	}

	tests := []struct {
		name string
		body string
	}{
		{
			"resourceSubscriptions is not an array",
			`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
				`{"notifications":{"resourceSubscriptions":"secret://tenant-b/payroll"}}}`,
		},
		{
			// JSON null decodes into a nil slice with no error, so without an
			// explicit check it reads as "no URIs" and passes unauthorized.
			"resourceSubscriptions is json null",
			`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
				`{"notifications":{"resourceSubscriptions":null}}}`,
		},
		{
			// Same type violation one level up; treated the same way.
			"notifications is json null",
			`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
				`{"notifications":null}}`,
		},
		{
			"resourceSubscriptions holds a non-string",
			`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
				`{"notifications":{"resourceSubscriptions":[{"uri":"secret://x"}]}}}`,
		},
		{
			"empty uri would authorize an empty identifier",
			listenBody(""),
		},
		{
			"more uris than the per-request cap",
			listenBody(tooMany...),
		},
		{
			// The cap counts entries as sent, before de-duplication, so it bounds
			// the work of de-duplicating them too. One URI repeated past the cap
			// is still refused.
			"one uri repeated past the per-request cap",
			listenBody(repeatedPastCap...),
		},
		{
			// A member outside the known set may be a future or vendor field that
			// names resources; ignoring it would reopen the bypass silently.
			"unrecognised notifications member",
			`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` +
				`{"notifications":{"resourceWatches":["secret://tenant-b/payroll"]}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runDerived(t, tt.body, allowAll)

			assert.Equal(t, http.StatusForbidden, got.status)
			assert.False(t, got.backendCalled, "malformed subscription must not reach the backend")
			assert.Empty(t, got.checks, "malformed subscription must not consult the authorizer")
		})
	}
}

// TestAlwaysAllowedMethodsArePinned closes the bug class both advisories belong
// to rather than just their two instances. An entry with an empty feature AND an
// empty operation skips the authorizer entirely, so the set of such methods is
// the proxy's complete unauthenticated attack surface at the MCP layer.
//
// Adding a method here is a security decision. If this test fails you have either
// added an always-allowed method -- in which case confirm it carries no capability
// identifier a policy could name, and say so in a comment beside the map entry --
// or you have correctly moved one out, in which case delete its line below.
func TestAlwaysAllowedMethodsArePinned(t *testing.T) {
	t.Parallel()

	// features/list is deliberately absent: it carries an empty feature but a
	// non-empty List operation, so it takes the list path rather than this early
	// return.
	want := []string{
		"initialize",
		"logging/setLevel",
		"notifications/cancelled",
		"notifications/initialized",
		"notifications/message",
		"notifications/progress",
		"notifications/prompts/list_changed",
		"notifications/resources/list_changed",
		"notifications/resources/updated",
		"notifications/roots/list_changed",
		"notifications/tasks/status",
		"notifications/tools/list_changed",
		"ping",
		"roots/list",
		"server/discover",
	}

	var got []string
	for method, featureOp := range MCPMethodToFeatureOperation {
		if featureOp.Feature == "" && featureOp.Operation == "" {
			got = append(got, method)
		}
	}
	sort.Strings(got)

	assert.Equal(t, want, got,
		"the set of methods that bypass the authorizer changed; see this test's doc comment")
}

// TestDerivedMethodsAreNotStaticallyClassified guards the wiring itself: one
// method, one vocabulary. A derived method that also carried a static
// MCPMethodToFeatureOperation entry would have two classifications that can
// disagree, and an empty pair there is exactly the bypass both advisories
// reported.
func TestDerivedMethodsAreNotStaticallyClassified(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, derivedAuthorizers, "derived authorizers must be wired")

	for method := range derivedAuthorizers {
		_, ok := MCPMethodToFeatureOperation[method]
		assert.False(t, ok,
			"%s is derived-authorized; remove its static MCPMethodToFeatureOperation entry", method)
	}
}

// TestDerivedChecksDoNotForwardRequestParamsAsArguments pins the fix for a bypass
// the per-capability checks themselves introduced.
//
// Cedar prefixes every argument key with "arg_" and merges the result into both
// the resource entity's attributes and the evaluation context, so the "arg_"
// namespace means "the arguments the authorized operation will run with".
// pkg/mcp hands completion/complete its ENTIRE params map as Arguments. Forwarding
// that would let a caller place any key at the top level of a completion request
// and have it evaluated as a prompt argument -- satisfying a policy condition the
// real prompts/get could never forge, because it passes only params.arguments.
func TestDerivedChecksDoNotForwardRequestParamsAsArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			"completion top-level params must not become arg_ attributes",
			`{"jsonrpc":"2.0","id":1,"method":"completion/complete","params":` +
				`{"ref":{"type":"ref/prompt","name":"restricted-deploy"},` +
				`"argument":{"name":"target","value":"p"},"env":"dev"}}`,
		},
		{
			"subscription params must not become arg_ attributes",
			listenBody("res://a"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := runDerived(t, tt.body, allowAll)

			require.Len(t, got.checks, 1)
			assert.Nil(t, got.checks[0].arguments,
				"derived checks must pass nil arguments; forwarding request params lets a "+
					"caller forge arg_* values for a policy condition")
		})
	}
}

// TestSubscriptionsListenNullAndEmptyShapes pins the boundary between "names no
// resource" and "type violation", which is subtle enough to regress silently.
//
// Absent and empty both unambiguously name nothing, so they carry no resource to
// authorize and pass through. An explicit JSON null contradicts the schema at
// whichever level it appears, and this middleware and the backend would each have
// to guess the same meaning for it -- so it is refused rather than coerced.
func TestSubscriptionsListenNullAndEmptyShapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		params     string
		wantStatus int
	}{
		{"notifications absent", `{}`, http.StatusOK},
		{"notifications empty object", `{"notifications":{}}`, http.StatusOK},
		{"resourceSubscriptions absent", `{"notifications":{"toolsListChanged":true}}`, http.StatusOK},
		{"resourceSubscriptions empty array", `{"notifications":{"resourceSubscriptions":[]}}`, http.StatusOK},
		{"resourceSubscriptions null", `{"notifications":{"resourceSubscriptions":null}}`, http.StatusForbidden},
		{"notifications null", `{"notifications":null}`, http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			body := `{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":` + tt.params + `}`
			got := runDerived(t, body, denyAll)

			assert.Equal(t, tt.wantStatus, got.status)
			assert.Equal(t, tt.wantStatus == http.StatusOK, got.backendCalled)
			assert.Empty(t, got.checks, "none of these shapes names a resource to authorize")
		})
	}
}

// slowAuthorizer blocks for delay on every decision, or until ctx is done.
type slowAuthorizer struct {
	delay time.Duration
	calls atomic.Int32
}

func (s *slowAuthorizer) AuthorizeWithJWTClaims(
	ctx context.Context, _ authorizers.MCPFeature, _ authorizers.MCPOperation,
	_ string, _ map[string]interface{},
) (bool, error) {
	s.calls.Add(1)
	select {
	case <-time.After(s.delay):
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// TestAuthorizeChecksBudget pins the aggregate deadline across a multi-decision
// request.
//
// The per-decision timeout in authorizers/http bounds one outbound call, not a
// request's worth of them, so without a shared budget a hanging PDP turns N
// decisions into N times that timeout. The budget makes the worst case
// independent of N, and stops the loop early rather than running every check.
func TestAuthorizeChecksBudget(t *testing.T) {
	t.Parallel()

	checks := make([]resourceCheck, 20)
	for i := range checks {
		checks[i] = resourceRead(fmt.Sprintf("res://%d", i))
	}

	t.Run("overrun denies and stops early", func(t *testing.T) {
		t.Parallel()
		slow := &slowAuthorizer{delay: 50 * time.Millisecond}

		start := time.Now()
		authorized, err := authorizeChecks(context.Background(), slow, checks, 120*time.Millisecond)
		elapsed := time.Since(start)

		assert.False(t, authorized)
		require.Error(t, err)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Less(t, elapsed, 20*50*time.Millisecond,
			"the budget must bound the request, not the sum of the per-call timeouts")
		assert.Less(t, int(slow.calls.Load()), len(checks),
			"evaluation must stop at the overrun rather than run every check")
	})

	t.Run("within budget every check runs", func(t *testing.T) {
		t.Parallel()
		slow := &slowAuthorizer{delay: time.Millisecond}

		authorized, err := authorizeChecks(context.Background(), slow, checks, 30*time.Second)

		require.NoError(t, err)
		assert.True(t, authorized)
		assert.Equal(t, int32(len(checks)), slow.calls.Load())
	})

	t.Run("a single decision is not wrapped in the budget", func(t *testing.T) {
		t.Parallel()
		// One decision has no fan-out to bound and is already limited by the
		// authorizer's own per-call timeout, which an operator may have configured
		// deliberately. A budget shorter than the work must not deny it.
		slow := &slowAuthorizer{delay: 40 * time.Millisecond}

		authorized, err := authorizeChecks(
			context.Background(), slow, []resourceCheck{resourceRead("res://solo")}, time.Nanosecond)

		require.NoError(t, err)
		assert.True(t, authorized, "the budget must not apply to a lone decision")
	})

	t.Run("an inherited cancellation is not mistaken for an overrun", func(t *testing.T) {
		t.Parallel()
		slow := &slowAuthorizer{delay: time.Hour}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := authorizeChecks(ctx, slow, checks, 30*time.Second)

		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled), "client hang-up surfaces as Canceled, not DeadlineExceeded")
	})
}
