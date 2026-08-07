// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package tokenexchange implements RFC 8693 token exchange for the authorization server.
// It provides validation of subject tokens that were issued by the same authorization
// server, enabling agents to act on behalf of users through delegation.
package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// allowedSignatureAlgorithms lists the signing algorithms accepted during JWT verification.
// This restricts which algorithms the validator will accept, preventing algorithm
// confusion attacks where an attacker might try to use a weaker algorithm.
var allowedSignatureAlgorithms = []jose.SignatureAlgorithm{
	jose.ES256,
	jose.ES384,
	jose.RS256,
	jose.RS384,
	jose.RS512,
	jose.EdDSA,
}

// ValidatedClaims holds the verified claims extracted from a subject token.
// All fields are populated from a successfully validated JWT that was issued
// by this authorization server.
type ValidatedClaims struct {
	// Subject is the user identity from the "sub" claim (required for delegation).
	Subject string
	// Issuer is the token issuer from the "iss" claim.
	Issuer string
	// Audience is the list of intended recipients from the "aud" claim.
	Audience []string
	// Expiry is the token expiration time from the "exp" claim.
	Expiry time.Time
	// IssuedAt is the token issuance time from the "iat" claim.
	IssuedAt time.Time
	// JWTID is the unique token identifier from the "jti" claim.
	JWTID string
	// Name is the user's display name from the custom "name" claim.
	Name string
	// Email is the user's email address from the custom "email" claim.
	Email string
	// ClientID is the OAuth client ID from the custom "client_id" claim.
	ClientID string
	// Scopes is the space-delimited scope string assembled from the "scope"
	// or "scp" claim. RFC 9068 §2.2.1 spells it "scope" as a JSON string, but
	// fosite's default JWT claims strategy (token/jwt/claims_jwt.go) writes
	// scopes as a JSON array under "scp" instead, unless ScopeField is
	// explicitly set to String or Both — this server does not set it, so a
	// genuine ToolHive-issued access token used as a subject token carries
	// "scp", not "scope". When both are present, "scope" wins. Empty if the
	// subject token carries neither claim.
	Scopes string
	// MayAct holds the authorized actor from the "may_act" claim (RFC 8693 §4.4).
	// Nil when the subject token does not carry a may_act claim.
	MayAct *MayActClaim
	// ExternalActor is the client identity that the external-issuer validation
	// path has already authorized for delegation, via a per-issuer actor-claim
	// allowlist match. It is set ONLY by that path (multi_issuer_validator.go),
	// after the resolved actor claim is confirmed present in the issuer's
	// AllowedActors — never populated from token claims by buildValidatedClaims
	// or assignClaim. It is empty for self-issued tokens and for external
	// tokens that carry a may_act claim (may_act is authoritative there
	// instead). A non-empty value means the validator has already authorized
	// this token's actor for delegation; it is not itself a raw claim.
	ExternalActor string
	// ExternalIssuer is set by the external-issuer validation path
	// (validateExternalToken in multi_issuer_validator.go) to that issuer's
	// IssuerURL, for EVERY external token it validates — unlike
	// ExternalActor, this is set regardless of whether the token carries a
	// may_act claim. It exists so the handler can record provenance (which
	// issuer a delegation actually originated from) even for a
	// may_act-bearing external token, which leaves ExternalActor unset.
	// Empty for self-issued tokens. Like ExternalActor, it is never
	// populated from token claims by buildValidatedClaims or assignClaim —
	// it comes from the already-validated issuer config the token matched,
	// not from anything token-supplied, so it cannot be spoofed via claims.
	ExternalIssuer string
	// AllowedDelegateClients is set for EVERY external token — like
	// ExternalIssuer and unlike ExternalActor, it does not depend on whether
	// the token carries a may_act claim (see validateExternalToken). That
	// matters: the may_act path bypasses the AllowedActors allowlist
	// entirely, so it is the path that most needs this restriction to still
	// apply. It is set to that issuer's configured
	// TrustedIssuer.AllowedDelegateClients, and is never populated
	// from token claims by buildValidatedClaims or assignClaim, and the
	// validator never compares it against anything: the validator does not
	// know the authenticated ToolHive client, so checkDelegationConsent
	// (handler.go) is the one that checks actorID against this list. Nil
	// means the issuer did not configure AllowedDelegateClients, which is
	// permissive — any ToolHive client may use the allowlisted external
	// actor, same as before this field existed.
	AllowedDelegateClients []string
	// Extra contains all non-standard claims not captured by other fields.
	Extra map[string]any
}

// MayActClaim represents the RFC 8693 §4.4 may_act claim from a subject token.
// It identifies the party authorized to act on behalf of the subject.
type MayActClaim struct {
	Sub string `json:"sub"`
	// Iss, when present, qualifies Sub's namespace. RFC 8693 §4.4: "the
	// combination of the two claims 'iss' and 'sub' are sometimes necessary
	// to uniquely identify an authorized actor." Without it, Sub alone could
	// name a party in some other issuer's namespace while still being
	// compared against a ToolHive client ID by checkDelegationConsent — a
	// namespace-confusion bug, not just a missing feature. validateMayActShape
	// requires Iss, when present, to equal this authorization server's own
	// issuer, so by the time checkDelegationConsent reads Sub it is
	// guaranteed to be in ToolHive's own client namespace.
	Iss string `json:"iss,omitempty"`
}

// Compile-time check that SelfIssuedTokenValidator implements SubjectTokenValidator.
var _ SubjectTokenValidator = (*SelfIssuedTokenValidator)(nil)

// SelfIssuedTokenValidator validates subject tokens presented during RFC 8693 token exchange.
// It verifies that the token was issued by this authorization server by checking
// the signature against the server's own JWKS, and validates standard JWT claims.
type SelfIssuedTokenValidator struct {
	publicJWKS       *jose.JSONWebKeySet
	issuer           string
	allowedAudiences []string
}

// NewSelfIssuedTokenValidator creates a new validator for subject tokens.
// The jwks parameter must be non-nil and contain only the authorization server's
// public signing keys (e.g. AuthorizationServerConfig.PublicJWKS) — the validator
// only ever verifies signatures, so it must not be handed private key material.
// The issuer parameter is the expected "iss" claim value. allowedAudiences is the
// set of audiences this server accepts in a subject token's "aud" claim; per the
// same secure default as AuthorizationServerConfig.AllowedAudiences, an empty
// allowedAudiences rejects every subject token rather than skipping the check.
func NewSelfIssuedTokenValidator(
	jwks *jose.JSONWebKeySet, issuer string, allowedAudiences []string,
) (*SelfIssuedTokenValidator, error) {
	if jwks == nil {
		return nil, fmt.Errorf("JWKS must not be nil")
	}
	if issuer == "" {
		return nil, fmt.Errorf("issuer must not be empty")
	}
	return &SelfIssuedTokenValidator{
		publicJWKS:       jwks,
		issuer:           issuer,
		allowedAudiences: allowedAudiences,
	}, nil
}

// Validate parses and verifies a raw JWT subject token.
// It checks the signature against the server's JWKS, validates issuer and audience,
// ensures the token is not expired, and requires a subject claim for delegation.
//
// The subject token's "aud" claim is checked against allowedAudiences, but this
// validator deliberately does not require that the authorization server itself
// (i.e. the token endpoint) be among those audiences. RFC 8693 leaves subject-token
// validation criteria out of scope, and ToolHive's vMCP flow legitimately exchanges
// tokens addressed to a downstream/upstream resource rather than the AS. The residual
// cross-resource risk is mitigated elsewhere: the token is still pinned to the
// server-wide allowedAudiences, and the handler enforces the requested resource
// against the client's registered audiences.
//
// Returns the validated claims on success, or a descriptive error on failure.
func (v *SelfIssuedTokenValidator) Validate(_ context.Context, rawToken string) (*ValidatedClaims, error) {
	parsedToken, err := jwt.ParseSigned(rawToken, allowedSignatureAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("subject token is not a valid JWT: %w", err)
	}

	// kidMatched is discarded here: it exists to let the external-issuer path
	// (validateExternalToken in multi_issuer_validator.go) decide whether an
	// unknown kid is worth an on-demand JWKS refresh. This validator's JWKS
	// never comes over HTTP — it's this authorization server's own
	// PublicJWKS() — so there is nothing to refresh.
	standardClaims, extraClaims, _, err := verifySignature(parsedToken, v.publicJWKS)
	if err != nil {
		return nil, err
	}

	// Validate issuer and expiry. Audience is intentionally excluded from
	// jwt.Expected here — go-jose's AnyAudience check is skipped entirely when
	// empty, which is the opposite of this server's secure default (empty
	// AllowedAudiences means no audience is permitted). Audience is checked
	// explicitly below so an empty allowlist fails closed.
	expected := jwt.Expected{
		Issuer: v.issuer,
	}
	if err := standardClaims.ValidateWithLeeway(expected, 0); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	if err := validateAudience(standardClaims.Audience, v.allowedAudiences); err != nil {
		return nil, fmt.Errorf("subject token claims validation failed: %w", err)
	}

	// Expiry is required for delegation — a subject token without an expiry
	// cannot be safely bounded by the delegation lifetime.
	if standardClaims.Expiry == nil {
		return nil, fmt.Errorf("subject token is missing required 'exp' claim")
	}

	// Subject is required for delegation — the token must identify the user.
	if standardClaims.Subject == "" {
		return nil, fmt.Errorf("subject token is missing required 'sub' claim")
	}

	// Reject a subject token that is actually an ID token — see
	// rejectIDTokenClaims's doc comment.
	if err := rejectIDTokenClaims(extraClaims); err != nil {
		return nil, err
	}

	// If may_act is present, it must be well-formed. A present-but-malformed
	// may_act is treated as an invalid token, not an absent one — fail closed
	// to prevent silent downgrade to client_id. requireIss is false here: see
	// validateMayActShape's doc comment for why the self-issued path doesn't
	// need it.
	if err := validateMayActShape(extraClaims, v.issuer, false); err != nil {
		return nil, err
	}

	return buildValidatedClaims(standardClaims, extraClaims), nil
}

// verifySignature attempts to verify the JWT signature using the provided public keys.
// If the JWT header names a key ID (kid) that matches a key in the JWKS, only that
// key is tried: a token naming a kid but signed by a different key is a spoofed-kid
// attempt and must fail, not fall back to verifying against the rest of the keyset.
// When there is no kid, or the kid doesn't match any key in the JWKS, every key whose
// declared "use"/"alg" (RFC 7517 §4.2/§4.4) is compatible with this signature is
// tried in turn — a key marked for a different use (e.g. "enc") or a different
// algorithm is skipped rather than tried, per RFC 8725 §3.12's recommendation to
// scope keys to a single purpose as a substitution defense. Returns on the first
// successful verification. On failure, the last verification error is wrapped.
//
// The returned kidMatched is true only when the token's kid header was found among
// publicJWKS's keys — regardless of whether the signature itself then verified. The
// external-issuer path (multi_issuer_validator.go) uses this to tell "this key
// genuinely isn't in our cached JWKS yet, possibly a rotation" (kidMatched false)
// apart from "this kid exists here but the signature is wrong" (kidMatched true,
// which a refresh can never fix and must not retry).
func verifySignature(
	token *jwt.JSONWebToken,
	publicJWKS *jose.JSONWebKeySet,
) (jwt.Claims, map[string]any, bool, error) {
	candidates := publicJWKS.Keys
	var kidMatched bool
	if len(token.Headers) > 0 && token.Headers[0].KeyID != "" {
		if matched := publicJWKS.Key(token.Headers[0].KeyID); len(matched) > 0 {
			candidates = matched
			kidMatched = true
		}
	}

	var headerAlg string
	if len(token.Headers) > 0 {
		headerAlg = token.Headers[0].Algorithm
	}

	var lastErr error
	var filtered int
	for _, key := range candidates {
		if key.Use != "" && key.Use != "sig" {
			filtered++
			continue
		}
		if key.Algorithm != "" && key.Algorithm != headerAlg {
			filtered++
			continue
		}
		var claims jwt.Claims
		extra := map[string]any{}
		err := token.Claims(key, &claims, &extra)
		if err == nil {
			return claims, extra, kidMatched, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return jwt.Claims{}, nil, kidMatched, fmt.Errorf("subject token signature verification failed: %w", lastErr)
	}
	if filtered > 0 {
		return jwt.Claims{}, nil, kidMatched, fmt.Errorf(
			"subject token signature verification failed: no compatible keys in JWKS "+
				"(%d key(s) present but all skipped by use/alg filter)",
			len(candidates))
	}
	return jwt.Claims{}, nil, kidMatched, fmt.Errorf("subject token signature verification failed: no keys in JWKS")
}

// validateAudience checks that tokenAudience intersects allowedAudiences.
// Per this server's secure default (see AuthorizationServerConfig.AllowedAudiences),
// an empty allowedAudiences means no audience is permitted, so the check fails
// closed rather than skipping validation.
func validateAudience(tokenAudience jwt.Audience, allowedAudiences []string) error {
	if len(allowedAudiences) == 0 {
		return fmt.Errorf("no audiences are configured on this server")
	}
	for _, aud := range tokenAudience {
		if slices.Contains(allowedAudiences, aud) {
			return nil
		}
	}
	return fmt.Errorf("token audience %v does not match any allowed audience", []string(tokenAudience))
}

// buildValidatedClaims constructs a ValidatedClaims from standard and extra claims.
func buildValidatedClaims(
	standard jwt.Claims,
	extra map[string]any,
) *ValidatedClaims {
	vc := &ValidatedClaims{
		Subject:  standard.Subject,
		Issuer:   standard.Issuer,
		Audience: []string(standard.Audience),
		JWTID:    standard.ID,
		Extra:    make(map[string]any),
	}

	if standard.Expiry != nil {
		vc.Expiry = standard.Expiry.Time()
	}
	if standard.IssuedAt != nil {
		vc.IssuedAt = standard.IssuedAt.Time()
	}

	// Extract well-known custom claims and collect the rest into Extra.
	// Standard JWT registered claims are filtered out since they are already
	// captured in the structured fields above.
	for k, val := range extra {
		assignClaim(vc, k, val)
	}

	// Fall back to "scp" only if "scope" (handled above) didn't already set
	// Scopes. This must run after the loop, not inside assignClaim's per-key
	// switch, because map iteration order is random — assignClaim can't tell
	// whether a not-yet-seen "scope" claim will still show up.
	//
	// Without this fallback, a genuine self-issued subject token (which
	// carries "scp", not "scope") reads as scope-less and any scoped
	// exchange fails with invalid_scope — scoped delegation against a
	// self-issued token was effectively unusable before this fallback.
	if vc.Scopes == "" {
		if scp, ok := extra["scp"]; ok {
			vc.Scopes = scpToScopeString(scp)
		}
	}

	return vc
}

// scpToScopeString normalizes an "scp" claim value into a space-delimited
// scope string. fosite's default JWT claims strategy writes "scp" as a JSON
// array (see the Scopes field doc comment), which json.Unmarshal decodes as
// []any — but a plain space-delimited string is accepted too, in case an
// issuer writes it that way instead. Non-string array elements are skipped
// rather than rejected: a malformed entry degrades to a smaller scope set
// instead of failing subject-token validation outright.
func scpToScopeString(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case []any:
		scopes := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				scopes = append(scopes, s)
			}
		}
		return strings.Join(scopes, " ")
	default:
		return ""
	}
}

// assignClaim routes one non-standard JWT claim onto its structured
// ValidatedClaims field, or into Extra if it isn't a well-known claim.
// Registered JWT claims (sub, iss, aud, exp, iat, nbf, jti) are dropped —
// they're already captured in buildValidatedClaims's structured fields.
// "scp" is likewise dropped here, though its value is read separately (see
// buildValidatedClaims's post-loop fallback) since its precedence relative
// to "scope" can't be decided from a single, order-independent key/value
// pair. Keep actorClaimsNotInExtra (multi_issuer_validator.go) in sync with
// the cases here.
func assignClaim(vc *ValidatedClaims, key string, val any) {
	switch key {
	case "name":
		if s, ok := val.(string); ok {
			vc.Name = s
		}
	case "email":
		if s, ok := val.(string); ok {
			vc.Email = s
		}
	case "client_id":
		if s, ok := val.(string); ok {
			vc.ClientID = s
		}
	case "scope":
		if s, ok := val.(string); ok {
			vc.Scopes = s
		}
	case "scp":
		// Handled by buildValidatedClaims after its call to assignClaim for
		// every key completes, so "scope" (if present) wins regardless of
		// map iteration order. Case exists here only to keep "scp" out of
		// Extra, like every other well-known claim below.
	case "may_act":
		if m, ok := val.(map[string]any); ok {
			if s, ok := m["sub"].(string); ok {
				mayAct := &MayActClaim{Sub: s}
				if iss, ok := m["iss"].(string); ok {
					mayAct.Iss = iss
				}
				vc.MayAct = mayAct
			}
		}
	case "sub", "iss", "aud", "exp", "iat", "nbf", "jti":
		// Skip registered JWT claims — already in structured fields.
	default:
		vc.Extra[key] = val
	}
}

// validateMayActShape rejects a present-but-malformed may_act claim. Both
// validation paths (self-issued here, external in multi_issuer_validator.go)
// call this before buildValidatedClaims: assignClaim silently drops a
// malformed may_act rather than surfacing it, and each path's fallback
// consent signal when may_act is absent (client_id here, the actor allowlist
// externally) is weaker than what a well-formed may_act would have granted.
// Letting a malformed claim fall through would silently widen consent
// instead of rejecting the token.
//
// selfIssuer is this authorization server's own issuer identifier. When
// may_act carries an "iss" member, RFC 8693 §4.4 uses it together with "sub"
// to identify the actor's namespace; this server only ever grants delegation
// to its own clients (checkDelegationConsent compares may_act.sub against a
// ToolHive client ID), so an "iss" naming any other issuer would mean sub is
// being read out of the wrong namespace. Requiring iss == selfIssuer when
// present — same fail-closed treatment as a malformed sub — prevents that
// namespace confusion.
//
// requireIss makes "iss" mandatory rather than merely constrained when
// present. The self-issued path passes false: the subject token's own "iss"
// already equals selfIssuer (Validate routed it here on exactly that basis),
// so may_act.iss would be redundant to require. The external path
// (multi_issuer_validator.go) passes true: there, an external issuer that
// emits no "iss" on may_act could otherwise authorize ANY ToolHive client by
// naming its ID in a bare may_act.sub, entirely bypassing this issuer's
// AllowedActors/AllowedDelegateClients allowlists — the one consent path
// that does. No major IdP is known to emit may_act at all (Microsoft Entra,
// Okta, and Google do not; Keycloak emits "act", a different claim), so
// reaching this path already requires an operator configuring a custom
// claim mapper for a nonstandard claim — an operator capable of adding "sub"
// to that mapper is assumed capable of adding "iss" too. That assumption is
// not verified against any real IdP's behavior in this repo; it is the
// rationale for requireIss, not a tested fact.
func validateMayActShape(extra map[string]any, selfIssuer string, requireIss bool) error {
	raw, ok := extra["may_act"]
	if !ok || raw == nil {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return errors.New("subject token has malformed 'may_act' claim: expected a JSON object")
	}
	if sub, ok := m["sub"].(string); !ok || sub == "" {
		return errors.New("subject token has malformed 'may_act' claim: missing or invalid 'sub'")
	}
	rawIss, present := m["iss"]
	if !present {
		if requireIss {
			return errors.New("subject token has malformed 'may_act' claim: missing required 'iss'")
		}
		return nil
	}
	iss, ok := rawIss.(string)
	if !ok || iss == "" {
		return errors.New("subject token has malformed 'may_act' claim: invalid 'iss'")
	}
	if iss != selfIssuer {
		return fmt.Errorf(
			"subject token has malformed 'may_act' claim: 'iss' %q does not match this authorization server's issuer",
			iss)
	}
	return nil
}

// rejectIDTokenClaims rejects a subject token that carries "at_hash" or
// "c_hash" — OIDC Core §3.3.2.11 / §3.3.2.10 define both for ID tokens only,
// never present on an access token. This is a sound POSITIVE detector (their
// presence proves an ID token) but is NOT EXHAUSTIVE: both claims are
// OPTIONAL even on a valid authorization-code-flow ID token, so an ID token
// that omits them passes this check undetected. A "typ: at+jwt" header check
// is not viable as a replacement: Entra v1/v2, Okta, and Google all emit bare
// "JWT" for access tokens too. Deliberately does NOT check "nonce": Microsoft
// Graph access tokens carry one, so rejecting on its presence would reject
// legitimate Entra access tokens.
//
// The other, complementary layer is the audience-shape check on the external
// path: TrustedIssuer.ExpectedAudience (multi_issuer_validator.go) is
// documented as MUST be a resource identifier, not a client ID, since an ID
// token's "aud" is the requesting client's own ID. That check is
// operator-dependent, not self-enforcing — see ExpectedAudience's doc
// comment and looksLikeResourceIdentifier's startup warning. Between the two,
// ID/access-token discrimination for an external issuer is partial: an
// operator who mis-sets ExpectedAudience to a client ID, presented with a
// real IdP whose ID tokens omit at_hash/c_hash (both legal per OIDC Core),
// has no remaining defense here. The blast radius is bounded elsewhere: an
// admitted ID token carries no "scope"/"scp" claim, so grantScopes
// (handler.go) either rejects the exchange with invalid_scope or grants a
// zero-scope delegated token.
//
// A third discriminator was considered and rejected: an ID token's "aud"
// MUST equal its own "azp"/authorized-party claim (OIDC Core §2, §3.1.3.7
// step 9), so rejecting a token whose "aud" equals the issuer's configured
// actor claim looked like a candidate. It is not shipped, because it also
// fires on a legitimate access token: an app whose API and client share one
// registration — e.g. an Entra v1 app that calls its own exposed API, or any
// setup where ExpectedAudience is misconfigured to the app's bare
// client/resource GUID, which is exactly the ambiguous case this file's
// audience-shape check already warns about — issues access tokens whose
// "aud" is that shared ID and whose azp/appid is the very same ID. Rejecting
// on aud == azp would then reject that provider's genuine access tokens, not
// just its ID tokens. A false-positive risk of that shape is worse than the
// gap it would close.
func rejectIDTokenClaims(extra map[string]any) error {
	if _, ok := extra["at_hash"]; ok {
		return errors.New("subject token carries an 'at_hash' claim: ID tokens are not accepted as subject tokens")
	}
	if _, ok := extra["c_hash"]; ok {
		return errors.New("subject token carries a 'c_hash' claim: ID tokens are not accepted as subject tokens")
	}
	return nil
}
