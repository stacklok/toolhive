// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/stacklok/toolhive/pkg/auth"
	"github.com/stacklok/toolhive/pkg/auth/sessionbinding"
	"github.com/stacklok/toolhive/pkg/vmcp"
)

// Binding is one vMCP's call binding, built from a Config: the clients whose
// tokens must carry an entry, the maximum call duration, the single-use record,
// the 401 challenge, and the calls this replica has seen return.
//
// The vMCP's call-binding check and its token validator share one Binding. Pass
// it as the server's CallBinding, and give TokenValidatorOption to the token
// validator in front of the vMCP (the vMCP's incoming auth factory does so when
// given the Binding). The validator then accepts the messages that belong to a
// call that outlives its token. Without it, they get the validator's 401 once
// the token expires, and the vMCP logs a warning the first time a call-bound
// tool call starts.
//
// Build a Binding with New only: its zero value is not usable.
type Binding struct {
	required        map[string]struct{}
	maxCallDuration time.Duration
	record          Record
	recordShared    bool
	challenge       auth.BearerChallenge

	// returned holds the calls whose request has returned on this replica but
	// that the record could not mark as ended.
	returned returnedCalls
	// validatorWired reports that TokenValidatorOption has been called.
	validatorWired atomic.Bool
}

// New validates cfg and returns the Binding it describes. It returns an error
// wrapping vmcp.ErrInvalidConfig for a negative MaxCallDuration or an empty
// client ID.
func New(cfg Config) (*Binding, error) {
	if cfg.MaxCallDuration < 0 {
		return nil, fmt.Errorf("%w: CallBinding.MaxCallDuration must not be negative", vmcp.ErrInvalidConfig)
	}
	if slices.Contains(cfg.Clients, "") {
		return nil, fmt.Errorf("%w: CallBinding.Clients must not contain an empty client ID", vmcp.ErrInvalidConfig)
	}
	required := make(map[string]struct{}, len(cfg.Clients))
	for _, client := range cfg.Clients {
		required[client] = struct{}{}
	}
	record := cfg.Record
	if record == nil {
		record = NewMemoryRecord()
	}
	challenge := cfg.Challenge
	challenge.Scopes = slices.Clone(challenge.Scopes)
	return &Binding{
		required:        required,
		maxCallDuration: cmp.Or(cfg.MaxCallDuration, DefaultMaxCallDuration),
		record:          record,
		recordShared:    cfg.Record != nil,
		challenge:       challenge,
	}, nil
}

// Token reads the call binding from an access token's validated claims, as
// FromClaims does with the Binding's required clients.
func (b *Binding) Token(claims map[string]any) (*Token, string) {
	return FromClaims(claims, b.required)
}

// ClientsListed reports whether any client must send an entry.
func (b *Binding) ClientsListed() bool { return len(b.required) > 0 }

// MaxCallDuration is how long a call keeps its record open.
func (b *Binding) MaxCallDuration() time.Duration { return b.maxCallDuration }

// Record is the single-use record. It is nil only for a Binding that New did
// not build.
func (b *Binding) Record() Record { return b.record }

// RecordShared reports whether the embedder supplied the record. A
// MemoryRecord made by New covers this replica only.
func (b *Binding) RecordShared() bool { return b.recordShared }

// Challenge is the realm, resource metadata and scope of the 401 challenge.
func (b *Binding) Challenge() auth.BearerChallenge { return b.challenge }

// CallOpen reports whether call, recorded under key, still has its request
// open at now and is younger than the maximum call duration.
func (b *Binding) CallOpen(key string, call Call, now time.Time) bool {
	return !call.Ended && !b.returned.has(key, now) && now.Before(call.Started.Add(b.maxCallDuration))
}

// MarkReturned remembers that the call recorded under key has returned on this
// replica, when the record could not mark it as ended, until expiry.
func (b *Binding) MarkReturned(key string, expiry, now time.Time) {
	b.returned.add(key, expiry, now)
}

// TokenValidatorOption returns the token-validator option that accepts, after
// a call-bound token expires, the requests AdmitExpiredToken admits, for up to
// the maximum call duration after its exp. Give it to the validator in front
// of the vMCP that serves this Binding.
func (b *Binding) TokenValidatorOption() auth.TokenValidatorOption {
	b.validatorWired.Store(true)
	return auth.WithExpiredTokenAdmission(b.maxCallDuration, func(r *http.Request, token auth.ExpiredToken) bool {
		return b.AdmitExpiredToken(r, token, time.Now())
	})
}

// ValidatorWired reports whether TokenValidatorOption has been called.
func (b *Binding) ValidatorWired() bool { return b.validatorWired.Load() }

// AdmitExpiredToken reports whether the token validator may accept r, whose
// token passed every check but exp, at now. It admits r only when all of these
// hold:
//   - the token carries an mcp_tool_call entry;
//   - its key has a call in the record, the call is open at now (see
//     CallOpen), and the call has a session (a Modern call has none);
//   - r names that session;
//   - r is a POST, a GET or a DELETE.
//
// A list call is recorded ended, so no request of a list-call token is ever
// admitted. The vMCP's call-binding check then admits only the call's own
// messages: a notifications/cancelled for the call's ID, a JSON-RPC response
// for a tool call, and a GET or DELETE on the call's session. It refuses
// anything else with 401, as the validator refuses an expired token.
//
// It never reads r's body. Why it refused is logged at DEBUG, as a caller can
// trigger a refusal at request rate; the jti is logged, never the subject.
func (b *Binding) AdmitExpiredToken(r *http.Request, expired auth.ExpiredToken, now time.Time) bool {
	ctx := r.Context()
	if b.record == nil {
		// Not built by New: nothing was recorded, so nothing is open.
		return false
	}
	token, reason := b.Token(expired.Claims)
	refused := func(why string) bool {
		jti := ""
		if token != nil {
			jti = token.JTI
		}
		slog.DebugContext(ctx, "call binding: an expired token was not admitted", "reason", why, "jti", jti)
		return false
	}
	switch {
	case reason != "":
		return refused(reason)
	case token == nil:
		return refused("no_entry")
	}
	switch r.Method {
	case http.MethodPost, http.MethodGet, http.MethodDelete:
	default:
		return refused(ReasonMethodNotAllowed)
	}
	call, found, err := b.record.Lookup(ctx, token.Key)
	switch {
	case err != nil:
		// DEBUG, as every refusal here: a caller with an expired token sets
		// the rate. The record's failure shows in the call-binding check's
		// own warnings.
		slog.DebugContext(ctx, "call binding: an expired token was not admitted: the record failed",
			"jti", token.JTI, "error", err)
		return false
	case !found || !b.CallOpen(token.Key, call, now):
		return refused(ReasonNoOpenCall)
	}
	sessionID, err := sessionbinding.RequestID(r, "")
	if err != nil || call.SessionID == "" || sessionID != call.SessionID {
		return refused(ReasonSessionMismatch)
	}
	return true
}

// returnedCalls is the set of calls, keyed by token key, whose request has
// returned on this replica but that the record could not mark as ended. Each
// entry lasts as long as the record keeps its key. Entries are added only
// when the record fails, and expired ones are dropped on each add.
type returnedCalls struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func (c *returnedCalls) add(key string, expiry, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.expires == nil {
		c.expires = make(map[string]time.Time)
	}
	maps.DeleteFunc(c.expires, func(_ string, e time.Time) bool { return !now.Before(e) })
	c.expires[key] = expiry
}

func (c *returnedCalls) has(key string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiry, ok := c.expires[key]
	return ok && now.Before(expiry)
}
