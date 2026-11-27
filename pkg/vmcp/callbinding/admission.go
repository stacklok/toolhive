// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ExpiredTokenAdmission lets one issuer's tokens for one client send their
// call's own messages after exp: a notifications/cancelled for the call's ID, a
// JSON-RPC response such as an elicitation answer, and a GET or DELETE on the
// call's session, while the call is open. Nothing else is accepted after exp,
// and a new call can still start only before exp.
//
// An entry matches a call-bound token whose iss claim equals Issuer and whose
// client_id claim equals ClientID, exactly. The azp claim is not read, so a
// token that names its client only in azp never matches. client_id names the
// client the token was issued to, not the one presenting it: on an
// authorization server that several clients share, an entry limits which
// tokens get the grace, not which clients can be issued a call entry.
type ExpiredTokenAdmission struct {
	// Issuer is the token's iss claim.
	Issuer string
	// ClientID is the token's client_id claim.
	ClientID string
	// Grace is how long after the token's exp its call's messages are still
	// accepted. It must be positive and at most the maximum call duration.
	Grace time.Duration
}

// ValidateExpiredTokenAdmission reports the first problem with entries, given
// the maximum call duration (zero means DefaultMaxCallDuration): an empty
// Issuer or ClientID, a Grace that is not positive or exceeds the maximum call
// duration, or two entries for the same Issuer and ClientID.
func ValidateExpiredTokenAdmission(entries []ExpiredTokenAdmission, maxCallDuration time.Duration) error {
	if maxCallDuration == 0 {
		maxCallDuration = DefaultMaxCallDuration
	}
	seen := make(map[admissionKey]int, len(entries))
	for i, e := range entries {
		switch {
		case strings.TrimSpace(e.Issuer) == "":
			return fmt.Errorf("entry %d: Issuer must not be empty", i)
		case strings.TrimSpace(e.ClientID) == "":
			return fmt.Errorf("entry %d: ClientID must not be empty", i)
		case e.Grace <= 0:
			return fmt.Errorf("entry %d: Grace must be positive", i)
		case e.Grace > maxCallDuration:
			return fmt.Errorf("entry %d: Grace %s exceeds the maximum call duration %s", i, e.Grace, maxCallDuration)
		}
		key := admissionKey{issuer: e.Issuer, clientID: e.ClientID}
		if first, dup := seen[key]; dup {
			return fmt.Errorf("entry %d: Issuer %q and ClientID %q are already listed by entry %d",
				i, e.Issuer, e.ClientID, first)
		}
		seen[key] = i
	}
	return nil
}

// admissionKey is an ExpiredTokenAdmission's issuer and client.
type admissionKey struct {
	issuer   string
	clientID string
}

// errNotListed and errGraceExceeded say why expiredTokenGrace refused.
var (
	errNotListed     = errors.New("not_listed")
	errGraceExceeded = errors.New("grace_exceeded")
)

// withinGrace reports nil when claims match an ExpiredTokenAdmission entry
// and now is less than its grace after expiredAt, and otherwise why not.
func (b *Binding) withinGrace(claims map[string]any, expiredAt, now time.Time) error {
	issuer, _ := claims["iss"].(string)
	clientID, _ := claims["client_id"].(string)
	grace, ok := b.admissions[admissionKey{issuer: issuer, clientID: clientID}]
	switch {
	case !ok || issuer == "" || clientID == "":
		return errNotListed
	case now.Sub(expiredAt) >= grace:
		return errGraceExceeded
	}
	return nil
}

// WithinExpiredTokenGrace reports whether a call-bound token with claims,
// which expired at expiredAt, may still send its call's messages at now: its
// iss and client_id match an ExpiredTokenAdmission entry, and now is less than
// that entry's grace after expiredAt. It does not check that the call is open.
func (b *Binding) WithinExpiredTokenGrace(claims map[string]any, expiredAt, now time.Time) bool {
	return b.withinGrace(claims, expiredAt, now) == nil
}

// AdmitsExpiredTokens reports whether any ExpiredTokenAdmission entry is
// configured. Without one, exp is strict.
func (b *Binding) AdmitsExpiredTokens() bool { return len(b.admissions) > 0 }
