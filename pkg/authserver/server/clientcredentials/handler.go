// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package clientcredentials provides SPIFFE-bound client credentials handling.
package clientcredentials

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Handler restricts Fosite's client credentials handler to authenticated SPIFFE
// clients and association policy. It delegates token lifetime and issuance to
// exactly one stock Fosite ClientCredentialsGrantHandler.
type Handler struct {
	stock fosite.TokenEndpointHandler
}

// Factory creates the SPIFFE-aware wrapper around Fosite's stock client
// credentials handler. It must only be installed for configured associations
// that permit client_credentials.
func Factory() server.Factory {
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		stock, ok := compose.OAuth2ClientCredentialsGrantFactory(config.Config, storage, strategy).(fosite.TokenEndpointHandler)
		if !ok {
			return nil, fmt.Errorf("fosite client credentials factory returned %T, not a token endpoint handler", stock)
		}
		return &Handler{stock: stock}, nil
	}
}

// CanHandleTokenEndpointRequest handles only the exact client_credentials grant.
func (*Handler) CanHandleTokenEndpointRequest(_ context.Context, requester fosite.AccessRequester) bool {
	return requester.GetGrantTypes().ExactOne(oauthproto.GrantTypeClientCredentials)
}

// CanSkipClientAuth requires authenticated SPIFFE client provenance.
func (*Handler) CanSkipClientAuth(_ context.Context, _ fosite.AccessRequester) bool { return false }

// HandleTokenEndpointRequest validates SPIFFE association policy, establishes
// the SPIFFE subject session, and delegates standard client-credentials work to
// Fosite.
func (h *Handler) HandleTokenEndpointRequest(ctx context.Context, requester fosite.AccessRequester) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}

	client, ok := requester.GetClient().(*registration.AuthenticatedSPIFFEClient)
	if !ok || client == nil || client.IsPublic() {
		return errorsx.WithStack(fosite.ErrInvalidGrant)
	}
	principal := client.Principal()
	policy := principal.AuthorizationPolicy()
	if !slices.Contains(policy.GrantTypes(), oauthproto.GrantTypeClientCredentials) ||
		!client.GetGrantTypes().Has(oauthproto.GrantTypeClientCredentials) {
		return errorsx.WithStack(fosite.ErrUnauthorizedClient)
	}
	if err := grantPolicyScopes(requester, policy.Scopes()); err != nil {
		return err
	}
	if err := grantPolicyResource(requester, policy.Resources()); err != nil {
		return err
	}

	sess := session.New(principal.SPIFFEID(), "", client.GetID(), session.UserClaims{})
	// Advertise the granted scopes per RFC 9068 Section 2.2.3. Read them back
	// from the requester rather than from the policy: grantPolicyScopes above
	// grants only what was both requested and permitted, so the policy set is
	// an upper bound rather than what this token actually carries.
	if granted := requester.GetGrantedScopes(); len(granted) > 0 {
		sess.JWTClaims.Extra[session.ScopeClaimKey] = strings.Join(granted, " ")
	}
	requester.SetSession(sess)
	return h.stock.HandleTokenEndpointRequest(ctx, requester)
}

// PopulateTokenEndpointResponse delegates access-token issuance to the stock
// Fosite handler after HandleTokenEndpointRequest has established the session.
func (h *Handler) PopulateTokenEndpointResponse(
	ctx context.Context, requester fosite.AccessRequester, responder fosite.AccessResponder,
) error {
	if !h.CanHandleTokenEndpointRequest(ctx, requester) {
		return errorsx.WithStack(fosite.ErrUnknownRequest)
	}
	return h.stock.PopulateTokenEndpointResponse(ctx, requester, responder)
}

func grantPolicyScopes(requester fosite.AccessRequester, permitted []string) error {
	for _, scope := range requester.GetRequestedScopes() {
		if !slices.Contains(permitted, scope) {
			return errorsx.WithStack(fosite.ErrInvalidScope)
		}
		requester.GrantScope(scope)
	}
	return nil
}

func grantPolicyResource(requester fosite.AccessRequester, permitted []string) error {
	form := requester.GetRequestForm()
	resources := form["resource"]
	if len(resources) != 1 || resources[0] == "" {
		return errorsx.WithStack(server.ErrInvalidTarget)
	}
	if len(form["audience"]) != 0 {
		return errorsx.WithStack(server.ErrInvalidTarget)
	}
	resource := resources[0]
	if err := server.ValidateAudienceURI(resource); err != nil {
		return errorsx.WithStack(server.ErrInvalidTarget)
	}
	if !slices.Contains(permitted, resource) {
		return errorsx.WithStack(server.ErrInvalidTarget)
	}
	requester.GrantAudience(resource)
	return nil
}

var _ fosite.TokenEndpointHandler = (*Handler)(nil)
