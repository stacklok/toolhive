// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/ory/fosite"

	servercrypto "github.com/stacklok/toolhive/pkg/authserver/server/crypto"
)

// clientAssertionSigningAlgorithms are the algorithms a private_key_jwt
// client may register, which are the ones fosite verifies.
var clientAssertionSigningAlgorithms = func() []jose.SignatureAlgorithm {
	supported := servercrypto.SupportedClientKeyAlgorithms()
	algs := make([]jose.SignatureAlgorithm, len(supported))
	for i, alg := range supported {
		algs[i] = jose.SignatureAlgorithm(alg)
	}
	return algs
}()

// clientAssertionAudienceConfig overrides the token URLs fosite accepts as the
// audience of a client assertion. fosite reads them only when authenticating
// clients; every other Configurator method comes from the embedded config.
type clientAssertionAudienceConfig struct {
	*fosite.Config
	tokenURLs []string
}

func (c clientAssertionAudienceConfig) GetTokenURLs(context.Context) []string {
	return c.tokenURLs
}

// newClientAssertionAudienceConfig wraps config so that fosite accepts the
// issuer, and nothing else, as the audience of a client assertion. fosite does
// not accept the issuer on its own, so it is handed over as a token URL. RFC
// 10070 section 4 requires the issuer identifier as the sole aud value and
// forbids the token endpoint URL.
func newClientAssertionAudienceConfig(config *fosite.Config) clientAssertionAudienceConfig {
	return clientAssertionAudienceConfig{
		Config:    config,
		tokenURLs: []string{config.AccessTokenIssuer},
	}
}

// clientAssertionStore is the part of fosite.Storage that the clock skew
// fallback uses to finish what fosite's strategy would have done. The store
// must be the clientAssertionExpiryStore, so that the marker it records gets the
// same retention as one fosite records.
type clientAssertionStore interface {
	GetClient(ctx context.Context, id string) (fosite.Client, error)
	ClientAssertionJWTValid(ctx context.Context, jti string) error
	SetClientAssertionJWT(ctx context.Context, jti string, exp time.Time) error
}

// newClientAssertionAudienceStrategy refuses a private_key_jwt client
// assertion whose aud is not issuer and nothing else, before next runs. The
// issuer may be sent as a string or as a one-element array, which RFC 7519
// section 4.1.3 and RFC 10070 both allow. fosite only checks that aud contains
// an accepted value, so without this an array such as [issuer, "other"] would
// pass. The refusal comes before any client lookup, so such an assertion costs
// no lookup and does not use up its jti.
//
// It applies to every strategy below it, fosite's own or one the caller
// supplied, the same way the JWS guard does. The clock skew recovery does not
// check aud itself and relies on this strategy sitting above it.
//
// An assertion that go-jose v4 cannot read is refused as well, instead of being
// left to next. fosite parses with go-jose v3, which strips spaces and tabs and
// trims base64 padding where v4 does not, so a token v4 rejects can still be
// read and verified by fosite, with an aud array this check never saw.
// Conforming clients send the canonical compact form and are not affected. The
// claims are read without verifying the signature, which is safe because the
// result can only refuse. The JWS guard above this strategy has already
// screened the assertion.
//
// A refusal is logged at WARN on purpose: it is a request shape that older
// releases accepted, and operators need to see why such a client now fails.
func newClientAssertionAudienceStrategy(
	next fosite.ClientAuthenticationStrategy,
	issuer string,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		if !isPrivateKeyJWTRequest(form) {
			return next(ctx, r, form)
		}
		if reason, err := checkClientAssertionAudience(form.Get("client_assertion"), issuer); err != nil {
			slog.Warn("refused client assertion", "reason", reason, "client_id", form.Get("client_id"))
			return nil, err
		}
		return next(ctx, r, form)
	}
}

// newClientAssertionClockSkewStrategy lets a private_key_jwt client assertion
// whose exp, iat or nbf is within clientAssertionClockSkew of now authenticate.
// fosite offers no leeway setting, and its jwt.TimeFunc is a process-wide
// variable, so fosite stays the validator and this wrapper only recovers from
// its time errors. It must sit below newClientAssertionAudienceStrategy, which
// checks aud for the recovery.
//
// recoverTimeErrors turns the recovery on. Only fosite's own strategy may have
// it, because the recovery relies on fosite verifying the signature before it
// validates the claims. A caller-supplied strategy has no such guarantee, so
// with recoverTimeErrors false its time errors are returned unchanged.
//
// An assertion that fosite accepts on its own is not inspected here. Its exp
// is judged where its replay marker is recorded (ValidateClientAssertionExp).
//
// It must sit inside newClientAssertionErrorStrategy, so that a storage failure
// recorded during the fallback's own jti check still surfaces as server_error.
//
// Deliberate deviations: the skew also applies to exp, which RFC 7523 section 3
// item 4 allows. The token exchange assertion checks stay strict because their
// exp bounds a token this server issues. A missing iat is treated as now,
// because iat is optional (RFC 7523 section 3, OIDC Core section 9).
func newClientAssertionClockSkewStrategy(
	next fosite.ClientAuthenticationStrategy,
	store clientAssertionStore,
	now func() time.Time,
	recoverTimeErrors bool,
) fosite.ClientAuthenticationStrategy {
	return func(ctx context.Context, r *http.Request, form url.Values) (fosite.Client, error) {
		if !isPrivateKeyJWTRequest(form) {
			return next(ctx, r, form)
		}
		assertion := form.Get("client_assertion")

		client, err := next(ctx, r, form)
		if err == nil {
			return client, nil
		}
		if !recoverTimeErrors || !isClientAssertionTimeError(err) {
			return client, err
		}
		// fosite verifies the signature before it validates the claims
		// (jwt.ParseWithClaims), and its key lookup has already required the
		// client, its private_key_jwt method, the pinned algorithm and a
		// registered key. A time error therefore means the signature is valid,
		// so the claims can be read without verifying them again. Tests pin
		// this order, because a different order in a later fosite release
		// would make these claims untrusted.
		return recoverSkewedClientAssertion(ctx, store, form, assertion, now, err)
	}
}

// recoverSkewedClientAssertion repeats the checks fosite stopped before, with
// the clock skew applied. fositeErr is fosite's time error, which describes a
// refusal on a claim outside the skew margin. A SetClientAssertionJWT error is
// returned unchanged. A GetClient error is refused as invalid_client. A storage
// failure while checking the jti is refused as invalid_client here, and becomes
// server_error in newClientAssertionErrorStrategy, which reads the failure the
// backend recorded. aud is not checked here: newClientAssertionAudienceStrategy,
// which sits above the clock skew strategy, has already required it to be the
// issuer.
func recoverSkewedClientAssertion(
	ctx context.Context,
	store clientAssertionStore,
	form url.Values,
	assertion string,
	now func() time.Time,
	fositeErr error,
) (fosite.Client, error) {
	clientID := form.Get("client_id")
	refuse := func(reason string, err error) (fosite.Client, error) {
		slog.Debug("client assertion refused after clock skew check", "reason", reason, "client_id", clientID)
		return nil, err
	}

	claims, ok := parseVerifiedClientAssertionClaims(assertion)
	if !ok {
		return refuse("claims unreadable", errClientAssertionUnreadable())
	}
	if clientID == "" {
		clientID = claims.Subject
	}
	if reason, refusalErr := checkClientAssertionTimes(claims, now(), fositeErr); reason != "" {
		return refuse(reason, refusalErr)
	}
	if reason, refusalErr := checkClientAssertionIdentity(claims, clientID); reason != "" {
		return refuse(reason, refusalErr)
	}

	// Look the client up before reserving the jti, so a failed lookup does not
	// burn it. fosite read the client too, and the record could have changed
	// since, so the auth method is checked again. The key and the algorithm pin
	// are not: fosite verified the signature against them on its own read.
	client, err := store.GetClient(ctx, clientID)
	if err != nil {
		return refuse("client lookup failed", fosite.ErrInvalidClient.WithWrap(err).WithDebug(err.Error()))
	}
	oidcClient, ok := client.(fosite.OpenIDConnectClient)
	if !ok || oidcClient.GetTokenEndpointAuthMethod() != "private_key_jwt" {
		return refuse("client no longer uses private_key_jwt", fosite.ErrInvalidClient.WithHint(
			"The requested OAuth 2.0 Client does not support client authentication method 'private_key_jwt'."))
	}

	if err := store.ClientAssertionJWTValid(ctx, claims.ID); err != nil {
		if errors.Is(err, fosite.ErrJTIKnown) {
			return refuse("jti already used", fosite.ErrInvalidClient.WithHint(
				"Claim 'jti' from 'client_assertion' MUST only be used once."))
		}
		return refuse("jti invalid", fosite.ErrInvalidClient.WithHint(
			"The 'jti' claim of the 'client_assertion' is invalid."))
	}
	// The client lookup and the jti check can take long, so the time claims are
	// judged again with a fresh clock read. The replay marker of a consumed
	// assertion outlives the margin by only clientAssertionReplayGrace. If it
	// expired while this request was waiting, the reservation above succeeded as
	// if the jti were new, but this read is then past the margin and refuses the
	// request. Nothing is recorded for a refused assertion.
	if clientAssertionTimesAcceptable(claims, now()) != assertionTimesOK {
		return refuse("time claims out of range after the jti check", fositeErr)
	}
	if err := store.SetClientAssertionJWT(ctx, claims.ID, claims.Expiry.Time()); err != nil {
		return nil, err
	}
	return client, nil
}

// checkClientAssertionTimes judges the time claims before the jti is reserved,
// so a refused assertion does not burn a reservation. It returns a refusal
// reason and error, or an empty reason when the claims are fine. The store
// wrapper applies ValidateClientAssertionExp again when it records the marker.
func checkClientAssertionTimes(claims jwt.Claims, now time.Time, fositeErr error) (string, error) {
	switch clientAssertionTimesAcceptable(claims, now) {
	case assertionTimesNoExp:
		return "exp missing", ValidateClientAssertionExp(time.Time{}, now)
	case assertionTimesExpBeforeIAT:
		return "exp before iat", errClientAssertionExpBeforeIAT()
	case assertionTimesOutsideSkew:
		return "time claims out of range", fositeErr
	case assertionTimesOK:
	}
	if err := ValidateClientAssertionExp(claims.Expiry.Time(), now); err != nil {
		return "exp out of range", err
	}
	return "", nil
}

// errClientAssertionExpBeforeIAT is the refusal for an exp that is before iat.
func errClientAssertionExpBeforeIAT() error {
	return fosite.ErrInvalidClient.WithHint("The 'client_assertion' must not have an 'exp' before its 'iat'.")
}

// errClientAssertionUnreadable is the refusal for claims that cannot be read.
func errClientAssertionUnreadable() error {
	return fosite.ErrInvalidClient.WithHint("The 'client_assertion' claims could not be read.")
}

// checkClientAssertionIdentity checks iss, sub and jti against what fosite
// requires. aud is checked before fosite runs, by checkClientAssertionAudience.
// It returns a refusal reason and error, or an empty reason when the claims are
// fine.
func checkClientAssertionIdentity(claims jwt.Claims, clientID string) (string, error) {
	if clientID == "" || claims.Issuer != clientID {
		return "iss does not match the client", fosite.ErrInvalidClient.WithHint(
			"Claim 'iss' from 'client_assertion' must match the 'client_id' of the OAuth 2.0 Client.")
	}
	if claims.Subject != clientID {
		return "sub does not match the client", fosite.ErrInvalidClient.WithHint(
			"Claim 'sub' from 'client_assertion' must match the 'client_id' of the OAuth 2.0 Client.")
	}
	if claims.ID == "" {
		return "jti missing", fosite.ErrInvalidClient.WithHint(
			"Claim 'jti' from 'client_assertion' must be set but is not.")
	}
	return "", nil
}

// errClientAssertionAudience is the refusal for an aud that is not the issuer
// and nothing else.
func errClientAssertionAudience(issuer string) error {
	return fosite.ErrInvalidClient.WithHintf(
		"Claim 'audience' from 'client_assertion' must hold only the authorization server's issuer '%s'.", issuer)
}

// checkClientAssertionAudience refuses an assertion that cannot be read, or
// whose aud is not issuer and nothing else. aud may be the issuer as a string
// or as a one-element array. The raw claim is read, so that a missing, null or
// non-string aud is refused rather than read as empty. It returns a refusal
// reason and error, or an empty reason when the aud is fine. The signature is
// not verified, so the result may only refuse.
func checkClientAssertionAudience(assertion, issuer string) (string, error) {
	token, err := jwt.ParseSigned(assertion, clientAssertionSigningAlgorithms)
	if err != nil {
		return "claims unreadable", errClientAssertionUnreadable()
	}
	var claims struct {
		Audience json.RawMessage `json:"aud"`
	}
	if token.UnsafeClaimsWithoutVerification(&claims) != nil {
		return "claims unreadable", errClientAssertionUnreadable()
	}
	aud, ok := soleAudience(claims.Audience)
	if !ok || aud == "" || aud != issuer {
		return "audience is not the issuer alone", errClientAssertionAudience(issuer)
	}
	return "", nil
}

// soleAudience returns the only value of a raw aud claim, which may be a
// string or an array of strings. ok is false when aud is missing, is not a
// string or an array of strings, or does not hold exactly one value.
func soleAudience(raw json.RawMessage) (aud string, ok bool) {
	if json.Unmarshal(raw, &aud) == nil {
		return aud, true
	}
	var auds []string
	if json.Unmarshal(raw, &auds) != nil || len(auds) != 1 {
		return "", false
	}
	return auds[0], true
}

// parseVerifiedClientAssertionClaims reads the claims of an assertion without
// verifying the signature. Callers only pass assertions that fosite has
// already verified. A check on claims that were not verified, which may only
// refuse, belongs in checkClientAssertionAudience instead.
func parseVerifiedClientAssertionClaims(assertion string) (jwt.Claims, bool) {
	var claims jwt.Claims
	parsed, err := jwt.ParseSigned(assertion, clientAssertionSigningAlgorithms)
	if err != nil || parsed.UnsafeClaimsWithoutVerification(&claims) != nil {
		return jwt.Claims{}, false
	}
	return claims, true
}

// assertionTimesVerdict is the result of clientAssertionTimesAcceptable.
type assertionTimesVerdict int

const (
	assertionTimesOK assertionTimesVerdict = iota
	// assertionTimesNoExp means exp is missing or not positive.
	assertionTimesNoExp
	// assertionTimesExpBeforeIAT means exp is before iat.
	assertionTimesExpBeforeIAT
	// assertionTimesOutsideSkew means exp, iat or nbf is beyond the margin.
	assertionTimesOutsideSkew
)

// clientAssertionTimesAcceptable applies the exp, iat and nbf margins. exp
// must be positive and not before iat. A non-positive iat or nbf counts as
// absent, like in fosite. It compares whole seconds, like fosite, and never
// adds to a claim, so a huge claim cannot overflow. A claim outside the margin
// is reported before the exp and iat order. How far exp may be ahead of now is
// not judged here but in ValidateClientAssertionExp.
func clientAssertionTimesAcceptable(claims jwt.Claims, now time.Time) assertionTimesVerdict {
	if claims.Expiry == nil {
		return assertionTimesNoExp
	}
	nowSec := now.Unix()
	skewSec := int64(clientAssertionClockSkew / time.Second)

	exp := claims.Expiry.Time().Unix()
	if exp <= 0 {
		return assertionTimesNoExp
	}
	if exp <= nowSec-skewSec {
		return assertionTimesOutsideSkew
	}

	if claims.IssuedAt != nil && claims.IssuedAt.Time().Unix() > 0 {
		iat := claims.IssuedAt.Time().Unix()
		if iat > nowSec+skewSec {
			return assertionTimesOutsideSkew
		}
		if exp < iat {
			return assertionTimesExpBeforeIAT
		}
	}
	if claims.NotBefore != nil && claims.NotBefore.Time().Unix() > nowSec+skewSec {
		return assertionTimesOutsideSkew
	}
	return assertionTimesOK
}
