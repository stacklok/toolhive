// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"log/slog"

	"github.com/stacklok/toolhive/pkg/authserver/claimnames"
	"github.com/stacklok/toolhive/pkg/authserver/subject"
)

const (
	hintSubjectNotResolved  = "The subject could not be resolved."
	hintUpstreamSubjectBad  = "The ID-JAG assertion has a missing or malformed upstream_subject claim."
	hintUpstreamSubjectFrgn = "The ID-JAG assertion upstream_subject does not name this server's " +
		"sign-in identity provider and subject claim."

	reasonResolverUnavailable = "subject_resolver_unavailable"
	reasonUpstreamSubjectBad  = "upstream_subject_invalid"
	reasonUpstreamSubjectFrgn = "upstream_subject_foreign"
)

// idJAGRedemption builds the redemption for a validated ID-JAG. For a resolve
// issuer it resolves the subject, then evaluates the actor policy with the
// resolved user, so every refusal happens before anything is consumed.
func (h *IDJAGHandler) idJAGRedemption(
	ctx context.Context, claims *ValidatedClaims, policy *JWTBearerGrantPolicy, clientID, resource string,
) (redemption, error) {
	d := redemption{
		claims:   claims,
		resource: resource,
		// The client, unlike the plain handler's, is the real authenticated
		// one, so no synthetic replacement happens.
		subject:       claimnames.QualifySubject(claims.Issuer, claims.Subject),
		replayPurpose: idJAGReplayPurpose,
		replayKey:     claims.JWTID,
		carryAct:      true,
	}
	if policy.SubjectMode != SubjectModeResolve {
		return d, nil
	}
	user, err := h.resolveSubject(ctx, claims, clientID)
	if err != nil {
		return redemption{}, err
	}
	if err := h.checkActorPolicy(ctx, claims, clientID, resource, user); err != nil {
		return redemption{}, err
	}
	d.subject = user
	d.delegatedAccess = true
	return d, nil
}

// resolveSubject maps the ID-JAG's upstream_subject to a local user ID. The
// resolver decides which issuers and claims it accepts, and reports the others
// with subject.ErrNotConfigured.
func (h *IDJAGHandler) resolveSubject(
	ctx context.Context, claims *ValidatedClaims, clientID string,
) (string, error) {
	if h.resolver == nil {
		h.logSubjectRefusal(ctx, claims.Issuer, clientID, reasonResolverUnavailable)
		return "", serverFailure(hintSubjectNotResolved, errors.New("no subject resolver configured"))
	}
	ref, ok := upstreamSubjectRef(claims.Extra[claimnames.UpstreamSubjectKey])
	if !ok {
		h.logSubjectRefusal(ctx, claims.Issuer, clientID, reasonUpstreamSubjectBad)
		return "", invalidAssertion(hintUpstreamSubjectBad)
	}
	user, err := h.resolver.Resolve(ctx, ref)
	switch {
	case err == nil && user != "":
		return user, nil
	case errors.Is(err, subject.ErrNotConfigured):
		h.logSubjectRefusal(ctx, claims.Issuer, clientID, reasonUpstreamSubjectFrgn)
		return "", invalidAssertion(hintUpstreamSubjectFrgn)
	case err == nil, errors.Is(err, subject.ErrUnknown), errors.Is(err, subject.ErrAmbiguous):
		h.logSubjectRefusal(ctx, claims.Issuer, clientID, reasonSubjectUnknown)
		return "", refuseSubjectUnknown()
	default:
		h.logSubjectRefusal(ctx, claims.Issuer, clientID, reasonResolverUnavailable)
		return "", serverFailure(hintSubjectNotResolved, err)
	}
}

// upstreamSubjectRef reads an upstream_subject claim value. It is ok only for
// an object whose iss, claim and value are all non-empty strings.
func upstreamSubjectRef(raw any) (subject.Ref, bool) {
	m, ok := raw.(map[string]any)
	if !ok {
		return subject.Ref{}, false
	}
	iss, _ := m["iss"].(string)
	claim, _ := m["claim"].(string)
	value, _ := m["value"].(string)
	if iss == "" || claim == "" || value == "" {
		return subject.Ref{}, false
	}
	return subject.Ref{Issuer: iss, Claim: claim, Value: value}, true
}

// logSubjectRefusal logs at Debug: the client already gets the refusal, so the
// line is only for diagnosis. It carries no subject, jti or upstream_subject
// value.
func (h *IDJAGHandler) logSubjectRefusal(ctx context.Context, issuer, clientID, reason string) {
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.DebugContext(ctx, "ID-JAG redemption refused: subject not resolved",
		"issuer", issuer, "client_id", clientID, "reason", reason)
}
