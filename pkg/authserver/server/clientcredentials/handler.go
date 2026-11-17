// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package clientcredentials provides SPIFFE-bound client credentials handling.
package clientcredentials

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/registration"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Handler restricts Fosite's client credentials handler to authenticated SPIFFE
// clients and association policy. It delegates token lifetime and issuance to
// exactly one stock Fosite ClientCredentialsGrantHandler.
type Handler struct {
	stock            fosite.TokenEndpointHandler
	allowedAudiences []string
}

// Factory creates the SPIFFE-aware wrapper around Fosite's stock client
// credentials handler. It must only be installed for configured associations
// that permit client_credentials.
func Factory() server.Factory {
	return func(config *server.AuthorizationServerConfig, storage fosite.Storage, strategy any) (any, error) {
		created := compose.OAuth2ClientCredentialsGrantFactory(config.Config, storage, strategy)
		stock, ok := created.(fosite.TokenEndpointHandler)
		if !ok {
			return nil, fmt.Errorf("fosite client credentials factory returned %T, not a token endpoint handler", created)
		}
		return &Handler{stock: stock, allowedAudiences: config.AllowedAudiences}, nil
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
	if !ok {
		clientID := ""
		if requester.GetClient() != nil {
			clientID = requester.GetClient().GetID()
		}
		return reject(clientID, fosite.ErrUnauthorizedClient, "client is not an authenticated SPIFFE client",
			"The client_credentials grant is available only to clients authenticated with a SPIFFE credential.")
	}
	// The grant check comes first so a client without the grant cannot probe
	// its scope and resource lists.
	if !client.GetGrantTypes().Has(oauthproto.GrantTypeClientCredentials) {
		return reject(client.GetID(), fosite.ErrUnauthorizedClient, "grant not permitted for association",
			"The client is not permitted to use the client_credentials grant.")
	}
	if err := grantPolicyScopes(requester, client.GetID(), client.GetScopes()); err != nil {
		return err
	}
	if err := server.GrantSingleResource(requester, client.Resources(), h.allowedAudiences); err != nil {
		slog.Debug("client_credentials request rejected",
			"client_id", client.GetID(), "reason", fosite.ErrorToRFC6749Error(err).HintField)
		return errorsx.WithStack(err)
	}

	sess := session.New(client.SPIFFEID().String(), "", client.GetID(), session.UserClaims{})
	// A workload credential never links to an upstream IdP login; say so in
	// the token. See claimnames.NoUpstreamSessionKey.
	sess.JWTClaims.Extra[claimnames.NoUpstreamSessionKey] = true
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

func grantPolicyScopes(requester fosite.AccessRequester, clientID string, permitted fosite.Arguments) error {
	for _, scope := range requester.GetRequestedScopes() {
		if !slices.Contains(permitted, scope) {
			return reject(clientID, fosite.ErrInvalidScope, "scope not permitted for association: "+scope,
				fmt.Sprintf("The requested scope %q is not permitted for this client.", scope))
		}
		requester.GrantScope(scope)
	}
	return nil
}

// reject logs the rejection reason at debug level, never the credential or
// request body, and returns base with a hint naming the cause.
func reject(clientID string, base *fosite.RFC6749Error, reason, hint string) error {
	slog.Debug("client_credentials request rejected", "client_id", clientID, "reason", reason)
	return errorsx.WithStack(base.WithHint(hint))
}

var _ fosite.TokenEndpointHandler = (*Handler)(nil)
