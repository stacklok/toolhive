// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/authserver/server/session"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

const testSessionTokenType = "urn:test:session"

// testSession is a subject the test-only source knows: the user, the client
// that owns it and the subject claims recorded for it.
type testSession struct {
	subject string
	owner   string
	claims  map[string]json.RawMessage
}

// testSessionSource is a second subject source, written only against the
// core's API: a session handle as the subject token, no grant and no store.
type testSessionSource struct {
	issuer   string
	signer   idJAGSigner
	sessions map[string]testSession
}

func (s testSessionSource) mint(
	ctx context.Context, clientID string, policy ConfiguredClientPolicy, form url.Values,
	responder fosite.AccessResponder, now time.Time,
) (mintedIDJAG, string, error) {
	if form.Get("subject_token_type") != testSessionTokenType {
		return mintedIDJAG{}, "", fosite.ErrInvalidRequest.WithHint("The 'subject_token_type' value is not supported.")
	}
	shape, err := checkIDJAGRequest(form, policy)
	if err != nil {
		return mintedIDJAG{}, "", err
	}
	sess, ok := s.sessions[form.Get("subject_token")]
	if !ok || sess.owner != clientID {
		return mintedIDJAG{}, "session_invalid", exactReason(fosite.ErrInvalidRequest.ErrorField, "session_invalid")
	}
	return respondWithIDJAG(ctx, s.signer, allowAllIDJAGPolicy, idJAGMint{
		Issuer:             s.issuer,
		RequestingClientID: clientID,
		Subject:            idJAGSubject{Subject: sess.subject, Claims: sess.claims},
		Target:             shape.Target,
		Resource:           shape.Resource,
	}, responder, now)
}

// TestIDJAGMintRedeemedByMultiIssuerValidator runs a second subject source
// through the core and redeems the result with IDJAGHandler backed by the real
// multi-issuer validator, which fetches the minting server's keys from a JWKS
// endpoint.
func TestIDJAGMintRedeemedByMultiIssuerValidator(t *testing.T) {
	t.Parallel()

	tj := newTestJWKS(t)
	jwksServer := startJWKSServer(t, tj)
	target := mintTestTarget()
	target.RequiredSubjectClaims = []string{"upstream_subject"}
	policy := ConfiguredClientPolicy{IDJAGTargets: map[string]IDJAGTarget{target.Issuer: target}}
	source := testSessionSource{
		issuer: mintTestIssuer,
		signer: newMintTestSigner(tj),
		sessions: map[string]testSession{
			"session-1": {subject: mintTestSubject, owner: mintTestRequesting, claims: sessionSubjectClaims(t)},
		},
	}

	form := url.Values{
		"subject_token":        {"session-1"},
		"subject_token_type":   {testSessionTokenType},
		"requested_token_type": {oauthproto.TokenTypeIDJAG},
		"audience":             {mintTestTargetIssuer},
		"resource":             {mintTestResource},
	}
	responder := fosite.NewAccessResponse()
	minted, reason, err := source.mint(context.Background(), mintTestRequesting, policy, form, responder, time.Now())
	require.NoError(t, err)
	require.Empty(t, reason)
	assert.Equal(t, oauthproto.TokenTypeIDJAG, responder.ToMap()["issued_token_type"])

	// AS-2 trusts the minting server as an issuer: its keys come from the
	// JWKS endpoint, and an ID-JAG must name AS-2's issuer as its audience.
	resolved, err := ResolveJWTBearerGrantPolicies([]TrustedIssuer{{
		IssuerURL: mintTestIssuer,
		JWKSURL:   jwksServer.URL + "/jwks",
		JWTBearerGrant: &JWTBearerGrantPolicy{
			MaxAssertionAge:   (2 * time.Minute).String(),
			AcceptedAudiences: []string{mintTestTargetIssuer},
			SubjectBindings: []JWTBearerSubjectBinding{
				{Subject: mintTestSubject, AllowedResources: []string{mintTestResource}},
			},
		},
	}})
	require.NoError(t, err)
	consumer := &recordingAssertionConsumer{}
	redeemer, err := newIDJAGIssuanceHandler(
		newMultiValidator(t, newTestJWKS(t), resolved),
		mintTestTargetIssuer+server.TokenEndpointPath,
		consumer,
		&fosite.Config{AccessTokenLifespan: time.Hour, AudienceMatchingStrategy: fosite.DefaultAudienceMatchingStrategy},
		&mockAccessTokenStrategy{},
		&mockAccessTokenStorage{},
		resolved,
		nil, nil, nil,
	)
	require.NoError(t, err)

	req := fosite.NewAccessRequest(&session.Session{})
	req.GrantTypes = fosite.Arguments{oauthproto.GrantTypeJWTBearer}
	req.Form = url.Values{"assertion": {minted.Token}, "resource": {mintTestResource}}
	req.Client = &fosite.DefaultClient{ID: mintTestTargetClient, Audience: []string{mintTestResource}}

	ctx := context.Background()
	require.True(t, redeemer.CanHandleTokenEndpointRequest(ctx, req), "the typ header routes the token to IDJAGHandler")
	require.NoError(t, redeemer.HandleTokenEndpointRequest(ctx, req))

	issued, ok := req.GetSession().(*session.Session)
	require.True(t, ok)
	assert.Equal(t, mintTestIssuer+"#"+mintTestSubject, issued.GetSubject())
	assert.Equal(t, minted.JTI, consumer.key, "the minted jti is the replay key")
	assert.Equal(t, fosite.Arguments{mintTestResource}, req.GetGrantedAudience())

	resp := fosite.NewAccessResponse()
	require.NoError(t, redeemer.PopulateTokenEndpointResponse(ctx, req, resp))
	assert.NotEmpty(t, resp.GetAccessToken())
}
