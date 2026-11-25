// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/stacklok/toolhive/pkg/authz/authorizers"
	"github.com/stacklok/toolhive/pkg/vmcp"
	"github.com/stacklok/toolhive/pkg/vmcp/aggregator"
)

const (
	forbiddenResourceURI = "harness://static/secret"
	publicResourceURI    = "harness://static/public"
)

var denyListPolicies = []string{
	`permit(principal, action == Action::"read_resource", resource);`,
	`forbid(principal, action == Action::"read_resource", resource == Resource::"` + forbiddenResourceURI + `");`,
}

// resourceCore builds a core whose routing table routes every uri, so a request
// that is not stopped by admission would reach the backend client.
func resourceCore(t *testing.T, authz *authorizers.Config, uris ...string) (VMCP, *coreMocks) {
	t.Helper()
	cfg, m := baseConfig(t)
	cfg.ServerName = "test-vmcp"
	cfg.Authz = authz

	resources := make(map[string]*vmcp.BackendTarget, len(uris))
	for _, uri := range uris {
		resources[uri] = backendTarget()
	}
	m.reg.EXPECT().List(gomock.Any()).
		Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
	m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
		RoutingTable: &vmcp.RoutingTable{Resources: resources},
	}, nil).AnyTimes()

	c, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, m
}

func TestCedarAdmission_ExactResourceURIsStillDecidedByPolicy(t *testing.T) {
	t.Parallel()

	c, m := resourceCore(t, cedarAuthzConfig(t, denyListPolicies...), forbiddenResourceURI, publicResourceURI)
	ctx := context.Background()
	id := cedarIdentity()

	_, err := c.ReadResource(ctx, id, forbiddenResourceURI)
	assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)

	want := &vmcp.ResourceReadResult{Contents: []vmcp.ResourceContent{{URI: publicResourceURI, Text: "hi"}}}
	m.client.EXPECT().ReadResource(gomock.Any(), gomock.Any(), publicResourceURI).Return(want, nil)
	got, err := c.ReadResource(ctx, id, publicResourceURI)
	require.NoError(t, err)
	assert.Equal(t, want.Contents, got.Contents)

	require.NoError(t, c.CheckResourceRead(ctx, id, publicResourceURI))

	ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: publicResourceURI}
	completion := &vmcp.CompletionResult{Values: []string{"x"}}
	m.client.EXPECT().Complete(gomock.Any(), gomock.Any(), ref, "a", "b", gomock.Any()).Return(completion, nil)
	gotCompletion, err := c.Complete(ctx, id, ref, "a", "b", nil)
	require.NoError(t, err)
	assert.Equal(t, completion, gotCompletion)
}

func TestAllowAllAdmission_DoesNotRejectNonCanonicalResourceURIs(t *testing.T) {
	t.Parallel()

	const uri = "harness://static/a b"
	c, m := resourceCore(t, nil, uri)
	ctx := context.Background()

	require.NoError(t, c.CheckResourceRead(ctx, nil, uri))

	want := &vmcp.ResourceReadResult{Contents: []vmcp.ResourceContent{{URI: uri, Text: "hi"}}}
	m.client.EXPECT().ReadResource(gomock.Any(), gomock.Any(), uri).Return(want, nil)
	got, err := c.ReadResource(ctx, nil, uri)
	require.NoError(t, err)
	assert.Equal(t, want.Contents, got.Contents)
}

func TestCedarAdmission_FilterResourcesDoesNotApplyURICheck(t *testing.T) {
	t.Parallel()

	adm := cedarAdmissionWith(t, denyListPolicies...)
	const spaced = "harness://static/a b"

	got, err := adm.FilterResources(context.Background(), cedarIdentity(), []vmcp.Resource{
		{URI: forbiddenResourceURI}, {URI: publicResourceURI}, {URI: spaced},
	})

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, publicResourceURI, got[0].URI)
	assert.Equal(t, spaced, got[1].URI)
}

func TestCedarAdmission_RejectedResourceURIIsNotLogged(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	authorizer := &mockAuthorizer{}
	adm := newCedarAdmission(authorizer,
		withLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	allowed, err := adm.AllowResourceRead(context.Background(), cedarIdentity(),
		&vmcp.Resource{URI: " " + forbiddenResourceURI})

	assert.False(t, allowed)
	assert.ErrorIs(t, err, authorizers.ErrInvalidResourceURI)
	assert.Empty(t, authorizer.calls, "no policy may be evaluated for a rejected URI")
	logs := buf.String()
	assert.Contains(t, logs, "level=WARN")
	assert.Contains(t, logs, "not in canonical form")
	assert.Contains(t, logs, "rule=bytes", "the warning names the failed rule")
	assert.NotContains(t, logs, "harness")
	assert.NotContains(t, logs, "secret")
}

// nonCanonicalResourceURIs are spellings a URL parser can resolve to
// forbiddenResourceURI, or other URIs not in canonical form. An exact string
// comparison never matches them, so a read refuses them. A completion ref is a
// template string authorized as sent, so only the ones with a space or control
// character apply to it.
var nonCanonicalResourceURIs = []string{
	" " + forbiddenResourceURI,
	forbiddenResourceURI + " ",
	forbiddenResourceURI + "\t",
	"harness://static/se\tcret",
	"harness://static/se\ncret",
	"harness://static/a b",
	"HARNESS://static/secret",
	"harness://static/./secret",
	"harness://static/x/../secret",
	"harness://static/%2e%2e/secret",
	"harness://static/%73ecret",
	"harness://static/secret%3a",
	"harness://u:@static/secret",
	"secret",
}

func uriName(uri string) string { return fmt.Sprintf("%q", uri) }

func TestCedarAdmission_ReadsRefuseNonCanonicalURIsWithoutEvaluatingPolicy(t *testing.T) {
	t.Parallel()

	for _, uri := range nonCanonicalResourceURIs {
		t.Run(uriName(uri), func(t *testing.T) {
			t.Parallel()
			authorizer := &mockAuthorizer{results: map[string]mockResult{uri: {authorized: true}}}
			_, m := baseConfig(t)
			c := checkCore(m, newCedarAdmission(authorizer))

			assert.ErrorIs(t, c.CheckResourceRead(context.Background(), cedarIdentity(), uri), vmcp.ErrAuthorizationFailed)
			assert.Empty(t, authorizer.calls, "no policy may be evaluated for a rejected URI")

			// The same URI through ReadResource must not reach the backend: the
			// mock client has no expectations.
			core, _ := resourceCore(t, cedarAuthzConfig(t, denyListPolicies...), uri)
			_, err := core.ReadResource(context.Background(), cedarIdentity(), uri)
			assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed)
		})
	}
}

func TestCedarAdmission_AllowResourceTemplateRead(t *testing.T) {
	t.Parallel()

	const template = "harness://static/{id}"

	t.Run("template is authorized as sent with one policy call", func(t *testing.T) {
		t.Parallel()
		authorizer := &mockAuthorizer{results: map[string]mockResult{template: {authorized: true}}}
		adm := newCedarAdmission(authorizer)

		for _, tmpl := range []string{template, "file:///{name}", "harness://{+path}", "HARNESS://static/{id}"} {
			authorizer.results[tmpl] = mockResult{authorized: true}
			authorizer.calls = nil

			ok, err := adm.AllowResourceTemplateRead(context.Background(), cedarIdentity(),
				&vmcp.ResourceTemplate{URITemplate: tmpl})

			require.NoError(t, err, tmpl)
			assert.True(t, ok, tmpl)
			require.Len(t, authorizer.calls, 1, tmpl)
			assert.Equal(t, tmpl, authorizer.calls[0].resourceID)
			assert.Equal(t, authorizers.MCPFeatureResource, authorizer.calls[0].feature)
			assert.Equal(t, authorizers.MCPOperationRead, authorizer.calls[0].operation)
		}
	})

	t.Run("refused without a policy call", func(t *testing.T) {
		t.Parallel()
		const controlMsg = "denied resource template with a control character or space"
		tests := []struct {
			name    string
			tmpl    string
			wantLog string
		}{
			{"trailing space", template + " x", controlMsg},
			{"leading space", " " + template, controlMsg},
			{"tab", "harness://static/{a\tb}", controlMsg},
			{"NUL", "\x00" + template, controlMsg},
			{"empty", "", "denied empty resource template"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				authorizer := &mockAuthorizer{}
				var buf bytes.Buffer
				adm := newCedarAdmission(authorizer,
					withLogger(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))

				ok, err := adm.AllowResourceTemplateRead(context.Background(), cedarIdentity(),
					&vmcp.ResourceTemplate{URITemplate: tc.tmpl})

				assert.False(t, ok)
				assert.ErrorIs(t, err, authorizers.ErrInvalidResourceURI)
				assert.Empty(t, authorizer.calls)
				assert.Contains(t, buf.String(), "level=WARN")
				assert.Contains(t, buf.String(), tc.wantLog)
				assert.NotContains(t, buf.String(), "static")
			})
		}
	})
}

func TestComplete_ResourceTemplateRefThroughCore(t *testing.T) {
	t.Parallel()

	const template = "harness://static/{id}"
	newCore := func(t *testing.T, authz *authorizers.Config) (VMCP, *coreMocks) {
		t.Helper()
		cfg, m := baseConfig(t)
		cfg.ServerName = "test-vmcp"
		cfg.Authz = authz
		m.reg.EXPECT().List(gomock.Any()).
			Return([]vmcp.Backend{{ID: testBackendID, HealthStatus: vmcp.BackendHealthy}}).AnyTimes()
		m.agg.EXPECT().AggregateCapabilities(gomock.Any(), gomock.Any()).Return(&aggregator.AggregatedCapabilities{
			RoutingTable: &vmcp.RoutingTable{
				ResourceTemplates: map[string]*vmcp.BackendTarget{template: backendTarget()},
			},
		}, nil).AnyTimes()
		c, err := New(cfg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c, m
	}
	ctx := context.Background()

	t.Run("template permitted by policy is not rejected by the URI grammar", func(t *testing.T) {
		t.Parallel()
		c, m := newCore(t, cedarAuthzConfig(t,
			`permit(principal, action == Action::"read_resource", resource == Resource::"`+template+`");`))
		ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: template}
		want := &vmcp.CompletionResult{Values: []string{"x"}}
		m.client.EXPECT().Complete(gomock.Any(), gomock.Any(), ref, "id", "a", gomock.Any()).Return(want, nil)

		got, err := c.Complete(ctx, cedarIdentity(), ref, "id", "a", nil)

		require.NoError(t, err)
		assert.Equal(t, want, got)
	})

	t.Run("template with a space or an empty template is refused", func(t *testing.T) {
		t.Parallel()
		c, _ := newCore(t, cedarAuthzConfig(t, denyListPolicies...))

		refused := []string{""}
		for _, uri := range nonCanonicalResourceURIs {
			if authorizers.CheckResourceTemplateURI(uri) != nil {
				refused = append(refused, uri)
			}
		}
		require.Greater(t, len(refused), 1, "the whitespace spellings must be part of the list")
		for _, uri := range refused {
			ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: uri}

			_, err := c.Complete(ctx, cedarIdentity(), ref, "id", "a", nil)

			assert.ErrorIs(t, err, vmcp.ErrAuthorizationFailed, "%q", uri)
		}
	})

	t.Run("without authorization a template and a non-canonical ref behave as before", func(t *testing.T) {
		t.Parallel()
		c, m := newCore(t, nil)
		ref := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: template}
		want := &vmcp.CompletionResult{Values: []string{"x"}}
		m.client.EXPECT().Complete(gomock.Any(), gomock.Any(), ref, "id", "a", gomock.Any()).Return(want, nil)

		got, err := c.Complete(ctx, nil, ref, "id", "a", nil)
		require.NoError(t, err)
		assert.Equal(t, want, got)

		// An unroutable, non-canonical ref is answered leniently, not refused.
		odd := vmcp.CompletionRef{Type: vmcp.CompletionRefTypeResource, URI: "HARNESS://static/a b"}
		got, err = c.Complete(ctx, nil, odd, "id", "a", nil)
		require.NoError(t, err)
		assert.Empty(t, got.Values)
	})
}
