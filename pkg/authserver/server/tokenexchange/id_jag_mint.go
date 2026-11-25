// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/server"
	"github.com/stacklok/toolhive/pkg/oauthproto"
)

// Reasons the minting core returns to the client as the whole
// error_description: invalid_request for the missing-claim and policy reasons,
// invalid_grant for reasonBoundElapsed. They are stable strings. An
// internal failure returns refuseInternal instead, which is for the caller's
// log only: the client sees the generic server_error.
const (
	reasonUpstreamSubjectMissing = "upstream_subject_missing"
	reasonSubIDMissing           = "sub_id_missing"
	reasonPolicyDenied           = "policy_denied"
	reasonBoundElapsed           = "bound_elapsed"
)

// idJAGSubjectClaim is what one configuration token of
// IDJAGTarget.RequiredSubjectClaims means: the claim name it is emitted under
// and the reason sent when the subject does not have it.
type idJAGSubjectClaim struct {
	wireKey       string
	missingReason string
}

// The tokens a target may list in RequiredSubjectClaims. They are the only
// valid ones; IsIDJAGSubjectClaimToken is the check to use.
const (
	IDJAGSubjectClaimUpstreamSubject = "upstream_subject"
	IDJAGSubjectClaimSubID           = "sub_id"
)

// IsIDJAGSubjectClaimToken reports whether token is a valid entry of
// IDJAGTarget.RequiredSubjectClaims.
func IsIDJAGSubjectClaimToken(token string) bool {
	_, ok := idJAGSubjectClaims[token]
	return ok
}

// idJAGSubjectClaims maps the configuration tokens to their claims. Its wire
// keys are the only subject claims an ID-JAG may carry.
var idJAGSubjectClaims = map[string]idJAGSubjectClaim{
	IDJAGSubjectClaimUpstreamSubject: {wireKey: claimnames.UpstreamSubjectKey, missingReason: reasonUpstreamSubjectMissing},
	IDJAGSubjectClaimSubID:           {wireKey: claimnames.SubjectIdentifierKey, missingReason: reasonSubIDMissing},
}

// idJAGReservedClaims are claim names the core sets itself or keeps for later
// use. A subject claim with one of these names is refused even if a later
// change puts it in the allowlist.
var idJAGReservedClaims = map[string]struct{}{
	"iss": {}, "sub": {}, "aud": {}, "client_id": {}, "jti": {}, "iat": {}, "exp": {}, "nbf": {},
	"resource": {}, "scope": {}, "authorization_details": {}, "act": {}, "cnf": {}, "tenant": {}, "aud_tenant": {},
}

// errIDJAGPolicyDenied is the error an idJAGPolicy returns, or wraps, to refuse
// a request. The client gets invalid_request with reasonPolicyDenied.
var errIDJAGPolicyDenied = errors.New("ID-JAG request denied by policy")

// idJAGRequestShape is the target and resource of a request that passed
// checkIDJAGRequest, and, for a client with an actor profile, the presented
// actor token. checkIDJAGRequest checks only that the token is present once;
// validateActor validates it later. The token type is not carried: checkActorParameters
// forces it to equal the profile's. The actor token is empty for a client
// without a profile.
type idJAGRequestShape struct {
	Target     IDJAGTarget
	Resource   string
	ActorToken string
}

// idJAGSubject is the user an ID-JAG is minted for: the sub claim and the
// subject claims available for the user, keyed by wire key. In an idJAGMint,
// Claims are all the claims the source has for the user; the core selects the
// ones the target requires. In an idJAGPolicyInput, Claims are only the
// selected ones.
type idJAGSubject struct {
	Subject string
	Claims  map[string]json.RawMessage
}

// actClaim is a single-level RFC 8693 act claim.
type actClaim struct {
	Issuer  string
	Subject string
}

// idJAGMint is everything needed to mint one ID-JAG. The token's client_id
// claim is Target.ClientID.
type idJAGMint struct {
	// Issuer is this authorization server's access-token issuer, the iss claim.
	Issuer string
	// RequestingClientID is the client authenticated at this authorization
	// server. The policy sees it; the token never carries it.
	RequestingClientID string
	// Subject carries every claim available for the user. respondWithIDJAG
	// selects from them with selectSubjectClaims; a claim the target does not
	// require is never signed or shown to the policy.
	Subject  idJAGSubject
	Target   IDJAGTarget
	Resource string
	Act      *actClaim
	// NotAfter caps exp; zero values are ignored.
	NotAfter []time.Time
	// AuthorizationDetails is the JSON array of granted entries, nil for none,
	// with the bytes the client sent (see grantIDJAGDetails). mintIDJAG signs
	// its encoding/json form as the authorization_details claim.
	AuthorizationDetails json.RawMessage
}

// mintedIDJAG is a signed ID-JAG. IssuedAt and ExpiresAt are whole seconds and
// equal its iat and exp claims.
type mintedIDJAG struct {
	Token     string
	JTI       string
	IssuedAt  time.Time
	ExpiresAt time.Time
	// AuthorizationDetails is the signed authorization_details claim, byte for
	// byte as in the token, nil when the ID-JAG has none.
	AuthorizationDetails json.RawMessage
}

// idJAGPolicyInput is what the policy decides on. ClientID is the client
// authenticated at this authorization server. Subject.Claims holds only the
// claims selected for the target.
type idJAGPolicyInput struct {
	ClientID string
	Subject  idJAGSubject
	Target   IDJAGTarget
	Resource string
	Act      *actClaim
}

// idJAGPolicy decides whether an ID-JAG may be minted. It returns nil to
// allow, an error matching errIDJAGPolicyDenied to refuse, and any other error
// for an internal failure. respondWithIDJAG treats a nil idJAGPolicy as an
// internal failure; a caller that permits every request passes
// allowAllIDJAGPolicy.
//
// The input is a deep copy: a policy may change it, and nothing it changes
// reaches the minted token or the caller's values. The policy's own error,
// for a denial or a failure, is wrapped as the cause of the returned error. It
// is never sent to the client.
type idJAGPolicy func(ctx context.Context, in idJAGPolicyInput) error

// allowAllIDJAGPolicy allows every request. A caller must pass it explicitly
// to permit every request; a nil policy fails closed.
func allowAllIDJAGPolicy(context.Context, idJAGPolicyInput) error { return nil }

// checkIDJAGRequest checks the request parameters of an ID-JAG request and
// finds its target. It reads only the form. subject_token is read as fosite
// reads it, from the first value only, and an empty first value counts as
// absent. For audience, resource and the refused parameters every value is
// checked: an empty value counts as absent, and a refused parameter is refused
// if any of its values is non-empty, since fosite reads only the first.
// authorization_details is not read here; decideIDJAG reads it after this
// check (see parseIDJAGDetails).
// audience and resource are read from the raw form, so a value with spaces is
// one value. More than one audience is invalid_request, as section 4.3.3 of
// draft-ietf-oauth-identity-assertion-authz-grant requires. The audience must
// equal a configured target issuer exactly; it is not normalized. A target the
// client has not configured, whether unknown or configured for another client,
// and a resource the target does not list all get the same invalid_target
// error. Unknown parameters are ignored.
//
// A client with an actor profile must send actor_token and actor_token_type
// instead of having them refused; see checkIDJAGExtraParameters. The target
// check comes after them.
//
// Every refusal here carries a hint, never a stable reason, so it returns only
// the error to send.
func checkIDJAGRequest(form url.Values, policy ConfiguredClientPolicy) (idJAGRequestShape, error) {
	if form.Get("subject_token") == "" {
		return idJAGRequestShape{}, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'subject_token' parameter is required for token exchange."))
	}
	audiences := nonEmptyValues(form["audience"])
	if len(audiences) == 0 {
		return idJAGRequestShape{}, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'audience' parameter is required when requesting an ID-JAG."))
	}
	if len(audiences) > 1 {
		return idJAGRequestShape{}, errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'audience' parameter must have exactly one value when requesting an ID-JAG."))
	}
	resources := nonEmptyValues(form["resource"])
	if len(resources) != 1 {
		return idJAGRequestShape{}, errorsx.WithStack(server.ErrInvalidTarget.WithHint(
			"The 'resource' parameter must have exactly one value when requesting an ID-JAG."))
	}
	actorToken, err := checkIDJAGExtraParameters(form, policy.ActorProfile)
	if err != nil {
		return idJAGRequestShape{}, err
	}
	target, ok := policy.IDJAGTargets[audiences[0]]
	if !ok || !slices.Contains(target.Resources, resources[0]) {
		return idJAGRequestShape{}, errorsx.WithStack(server.ErrInvalidTarget.WithHint(
			"The 'audience' and 'resource' parameters do not name a target this client may request an ID-JAG for."))
	}
	return idJAGRequestShape{Target: target, Resource: resources[0], ActorToken: actorToken}, nil
}

// checkIDJAGExtraParameters checks the parameters beyond subject_token, audience
// and resource, checking every value of each. scope is refused for every
// client. authorization_details is not checked here: see parseIDJAGDetails.
//
// Without a profile, actor_token and actor_token_type are refused too, and the
// returned actor token is empty. With a profile they are required instead, by
// checkActorParameters, and the actor token is returned.
func checkIDJAGExtraParameters(form url.Values, profile *ActorProfile) (string, error) {
	if len(nonEmptyValues(form["scope"])) > 0 {
		return "", errorsx.WithStack(fosite.ErrInvalidScope.WithHint(
			"The 'scope' parameter is not supported when requesting an ID-JAG."))
	}
	if profile != nil {
		return checkActorParameters(form, profile)
	}
	if len(nonEmptyValues(form["actor_token"])) > 0 {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token' parameter is not supported when requesting an ID-JAG."))
	}
	// actor_token has no non-empty value here, so any actor_token_type is alone
	// (RFC 8693 section 2.1).
	if len(nonEmptyValues(form["actor_token_type"])) > 0 {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token_type' parameter must not be given without 'actor_token'."))
	}
	return "", nil
}

// checkActorParameters requires the actor token and its type for a client with
// a profile. "Exactly one" counts every value, empty ones included: the
// parameter must appear once, and that value must contain a character other
// than white space. The value is passed on unchanged. A repeated
// parameter is refused even when only one of its values is non-empty, since
// fosite reads only the first. actor_token_type must also equal the profile's
// type exactly. Nothing from the request is echoed.
func checkActorParameters(form url.Values, profile *ActorProfile) (string, error) {
	tokens := form["actor_token"]
	if len(tokens) != 1 || strings.TrimSpace(tokens[0]) == "" {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token' parameter is required, once, for this client."))
	}
	if types := form["actor_token_type"]; len(types) != 1 || types[0] != profile.ActorTokenType {
		return "", errorsx.WithStack(fosite.ErrInvalidRequest.WithHint(
			"The 'actor_token_type' parameter must name the token type configured for this client, once."))
	}
	return tokens[0], nil
}

// selectSubjectClaims picks the subject claims the target requires from the
// claims available for the subject, keyed by wire key. A required claim that
// is not available is refused with its stable reason (<token>_missing), as
// invalid_request with the reason as the whole description; an unknown
// required token is a server_error with reason refuseInternal, which is for the
// caller's log and not sent to the client. Available claims the target
// does not require are not copied. respondWithIDJAG calls it before the policy.
func selectSubjectClaims(
	target IDJAGTarget, available map[string]json.RawMessage,
) (map[string]json.RawMessage, string, error) {
	selected := make(map[string]json.RawMessage, len(target.RequiredSubjectClaims))
	for _, token := range target.RequiredSubjectClaims {
		claim, ok := idJAGSubjectClaims[token]
		if !ok {
			return nil, refuseInternal, serverFailure(hintMintFailed, fmt.Errorf("unknown required subject claim %q", token))
		}
		value := bytes.TrimSpace(available[claim.wireKey])
		if len(value) == 0 || bytes.Equal(value, []byte("null")) {
			return nil, claim.missingReason, errorsx.WithStack(
				exactReason(fosite.ErrInvalidRequest.ErrorField, claim.missingReason))
		}
		selected[claim.wireKey] = slices.Clone(json.RawMessage(value))
	}
	return selected, "", nil
}

// idJAGSigner signs ID-JAG claims. *server.Signer implements it; tests fake it to exercise signing failures.
type idJAGSigner interface {
	SignIDJAG(ctx context.Context, claims map[string]any) (string, error)
}

var _ idJAGSigner = (*server.Signer)(nil)

// respondWithIDJAG is the single entry point that mints an ID-JAG. It selects
// the subject claims the target requires from m.Subject.Claims, runs the
// policy on the selected claims, mints the token and writes the response. A nil
// policy is an internal failure; allowAllIDJAGPolicy permits every request.
// Claim selection and the policy always run before signing. It returns the
// minted token, and on refusal the reason with the error to send: a stable
// reason for a client refusal, refuseInternal for an internal failure. The
// error of an internal failure carries the cause as its debug field, which the
// token endpoint logs; respondWithIDJAG does not log.
func respondWithIDJAG(
	ctx context.Context, signer idJAGSigner, policy idJAGPolicy,
	m idJAGMint, responder fosite.AccessResponder, now time.Time,
) (mintedIDJAG, string, error) {
	if policy == nil {
		return mintedIDJAG{}, refuseInternal, serverFailure(hintMintFailed, errors.New("ID-JAG policy not configured"))
	}
	selected, reason, err := selectSubjectClaims(m.Target, m.Subject.Claims)
	if err != nil {
		return mintedIDJAG{}, reason, err
	}
	err = policy(ctx, idJAGPolicyInput{
		ClientID: m.RequestingClientID,
		Subject:  idJAGSubject{Subject: m.Subject.Subject, Claims: cloneClaimValues(selected)},
		Target:   cloneIDJAGTarget(m.Target),
		Resource: m.Resource,
		Act:      cloneActClaim(m.Act),
	})
	switch {
	case errors.Is(err, errIDJAGPolicyDenied):
		return mintedIDJAG{}, reasonPolicyDenied, errorsx.WithStack(
			exactReason(fosite.ErrInvalidRequest.ErrorField, reasonPolicyDenied).WithWrap(err))
	case err != nil:
		return mintedIDJAG{}, refuseInternal, serverFailure(hintMintFailed, fmt.Errorf("ID-JAG policy failed: %w", err))
	}
	if reason, err := checkIDJAGBoundsOpen(m, now); err != nil {
		return mintedIDJAG{}, reason, err
	}
	minted, err := mintIDJAG(ctx, signer, m, selected, now)
	if err != nil {
		return mintedIDJAG{}, refuseInternal, err
	}
	setIDJAGResponse(responder, minted)
	return minted, "", nil
}

// checkIDJAGBoundsOpen refuses a mint whose exp would not be after now. A
// target lifetime that does not reach the next second is an operator fault, a
// server_error. When a NotAfter cap pushes exp down to now, the grant behind the
// request has expired: that is invalid_grant with reasonBoundElapsed. exp is
// never rounded up, so a cap less than one second away is refused too.
// invalid_grant is deliberate. RFC 6749 section 5.2 covers an expired grant,
// and the sibling paths (the expired subject token in handler.go and
// issueBoundedAccessToken) answer the same way, although RFC 8693 section
// 2.2.2 could be read as pointing to invalid_request for an invalid
// subject_token.
func checkIDJAGBoundsOpen(m idJAGMint, now time.Time) (string, error) {
	if now.Add(m.Target.Lifetime).Unix() <= now.Unix() {
		return refuseInternal, serverFailure(hintMintFailed,
			fmt.Errorf("target lifetime %s does not reach the next second", m.Target.Lifetime))
	}
	if idJAGExpiry(now, m.Target.Lifetime, m.NotAfter) <= now.Unix() {
		return reasonBoundElapsed, errorsx.WithStack(
			exactReason(fosite.ErrInvalidGrant.ErrorField, reasonBoundElapsed))
	}
	return "", nil
}

// mintIDJAG builds the claims of an ID-JAG and signs them through the
// idJAGSigner, which sets the typ oauth-id-jag+jwt. aud is the target issuer as
// configured, as a JSON string; resource is a JSON string; iat and exp are
// integers; exp is the floor of the earliest of now plus the target lifetime
// and every non-zero NotAfter. There is no nbf, and act is present only when
// set. authorization_details is present only when m.AuthorizationDetails is
// set, as its encoding/json form: compacted, with <, > and & escaped, and every
// digit of a number kept. Every error is a server_error with no detail for
// the client; the cause is its debug field, which the token endpoint logs.
// It signs only the selected subject claims, never m.Subject.Claims. It checks
// each claim's name against the allowlist and the reserved names, and its value
// shape, but not that the claims match the target's RequiredSubjectClaims:
// respondWithIDJAG selects the claims for the target before it calls mintIDJAG.
func mintIDJAG(
	ctx context.Context, signer idJAGSigner, m idJAGMint, selected map[string]json.RawMessage, now time.Time,
) (mintedIDJAG, error) {
	if err := checkIDJAGMint(m); err != nil {
		return mintedIDJAG{}, serverFailure(hintMintFailed, err)
	}
	claims, err := idJAGSubjectClaimSet(selected)
	if err != nil {
		return mintedIDJAG{}, serverFailure(hintMintFailed, err)
	}

	issuedAt := now.Unix()
	exp := idJAGExpiry(now, m.Target.Lifetime, m.NotAfter)
	if !time.Unix(exp, 0).After(now) {
		return mintedIDJAG{}, serverFailure(hintMintFailed, errors.New("exp is not after now"))
	}

	jti, err := randomJTI()
	if err != nil {
		return mintedIDJAG{}, serverFailure(hintMintFailed, fmt.Errorf("jti: %w", err))
	}

	claims["iss"] = m.Issuer
	claims["sub"] = m.Subject.Subject
	claims["aud"] = m.Target.Issuer
	claims["client_id"] = m.Target.ClientID
	claims["jti"] = jti
	claims["iat"] = issuedAt
	claims["exp"] = exp
	claims["resource"] = m.Resource
	if m.Act != nil {
		claims["act"] = map[string]string{"iss": m.Act.Issuer, "sub": m.Act.Subject}
	}
	var details json.RawMessage
	if m.AuthorizationDetails != nil {
		// The signer encodes the claims with encoding/json, which compacts a
		// json.RawMessage and escapes <, > and &. Encoding it here first gives
		// the exact bytes the token carries, for the response to echo; numbers
		// keep every digit.
		if details, err = json.Marshal(m.AuthorizationDetails); err != nil {
			return mintedIDJAG{}, serverFailure(hintMintFailed, fmt.Errorf("authorization_details: %w", err))
		}
		claims[claimnames.AuthorizationDetailsKey] = details
	}

	token, err := signer.SignIDJAG(ctx, claims)
	if err != nil {
		return mintedIDJAG{}, serverFailure(hintMintFailed, fmt.Errorf("signing: %w", err))
	}
	return mintedIDJAG{
		Token: token, JTI: jti, IssuedAt: time.Unix(issuedAt, 0), ExpiresAt: time.Unix(exp, 0),
		AuthorizationDetails: details,
	}, nil
}

// setIDJAGResponse writes the RFC 8693 response for a minted ID-JAG. It sets
// no refresh_token and no scope. When the ID-JAG carries an
// authorization_details claim, the response returns the same entries in its
// authorization_details member (draft section 4.3.4).
func setIDJAGResponse(r fosite.AccessResponder, t mintedIDJAG) {
	r.SetAccessToken(t.Token)
	r.SetTokenType(oauthproto.TokenTypeNotApplicable)
	r.SetExpiresIn(t.ExpiresAt.Sub(t.IssuedAt))
	r.SetExtra(oauthproto.ParamIssuedTokenType, oauthproto.TokenTypeIDJAG)
	if t.AuthorizationDetails != nil {
		r.SetExtra(authorizationDetailsParam, t.AuthorizationDetails)
	}
}

// checkIDJAGMint refuses a mint that would give a token without a user, an
// issuer, an audience, a client or a resource, with a partial act claim, or
// with authorization_details that is not a non-empty JSON array of objects.
func checkIDJAGMint(m idJAGMint) error {
	switch {
	case m.Issuer == "":
		return errors.New("empty issuer")
	case m.Subject.Subject == "":
		return errors.New("empty subject")
	case m.Target.Issuer == "":
		return errors.New("empty target issuer")
	case m.Target.ClientID == "":
		return errors.New("empty client_id")
	case m.Resource == "":
		return errors.New("empty resource")
	case m.Act != nil && (m.Act.Issuer == "" || m.Act.Subject == ""):
		return errors.New("incomplete act claim")
	}
	if m.AuthorizationDetails != nil {
		if _, ok := signedEntries(m.AuthorizationDetails); !ok {
			return errors.New("authorization_details is not a non-empty array of objects")
		}
	}
	return nil
}

// idJAGSubjectClaimSet starts the claim set from the subject claims. Each claim
// is checked in this order: the name must not be reserved, then it must be on
// the allowlist, then the value must not be empty, whitespace-only or JSON null,
// then must be valid JSON, then must be a JSON object. Any refusal is a server_error.
func idJAGSubjectClaimSet(subjectClaims map[string]json.RawMessage) (map[string]any, error) {
	claims := make(map[string]any, len(subjectClaims)+9)
	for name, value := range subjectClaims {
		if _, reserved := idJAGReservedClaims[name]; reserved {
			return nil, fmt.Errorf("subject claim %q is reserved", name)
		}
		if !isIDJAGSubjectClaim(name) {
			return nil, fmt.Errorf("subject claim %q is not allowed", name)
		}
		if trimmed := bytes.TrimSpace(value); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return nil, fmt.Errorf("subject claim %q is empty or null", name)
		}
		if !json.Valid(value) {
			return nil, fmt.Errorf("subject claim %q is not valid JSON", name)
		}
		if bytes.TrimSpace(value)[0] != '{' {
			return nil, fmt.Errorf("subject claim %q is not a JSON object", name)
		}
		claims[name] = slices.Clone(value)
	}
	return claims, nil
}

// idJAGExpiry is exp in whole seconds: the floor of the earliest of now plus
// lifetime and every non-zero cap.
func idJAGExpiry(now time.Time, lifetime time.Duration, notAfter []time.Time) int64 {
	// A fresh slice, so the caller's NotAfter is never written to.
	return earliest(append([]time.Time{now.Add(lifetime)}, notAfter...)).Unix()
}

// hintMintFailed is the client-visible hint when an ID-JAG could not be minted.
const hintMintFailed = "The ID-JAG could not be minted."

func randomJTI() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return id.String(), nil
}

func isIDJAGSubjectClaim(name string) bool {
	for _, claim := range idJAGSubjectClaims {
		if claim.wireKey == name {
			return true
		}
	}
	return false
}

func nonEmptyValues(values []string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func cloneIDJAGTarget(t IDJAGTarget) IDJAGTarget {
	t.Resources = slices.Clone(t.Resources)
	t.RequiredSubjectClaims = slices.Clone(t.RequiredSubjectClaims)
	return t
}

func cloneClaimValues(claims map[string]json.RawMessage) map[string]json.RawMessage {
	if claims == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(claims))
	for name, value := range claims {
		out[name] = slices.Clone(value)
	}
	return out
}

func cloneActClaim(a *actClaim) *actClaim {
	if a == nil {
		return nil
	}
	c := *a
	return &c
}
