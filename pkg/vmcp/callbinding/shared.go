// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package callbinding

import (
	"cmp"
	"context"
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
// tokens must carry an entry, the maximum call duration, the issuers and
// clients whose tokens may send their call's messages after exp, the
// single-use record, the 401 challenge, and the calls this replica has seen
// return.
//
// The vMCP's call-binding check and its token validator share one Binding. Pass
// it as the server's CallBinding, and give TokenValidatorOption to the token
// validator in front of the vMCP (the vMCP's incoming auth factory does so when
// given the Binding). With Config.ExpiredTokenAdmission set, the validator then
// accepts the messages that belong to a call that outlives its token, for the
// matching entry's grace. Without the list, exp is strict. With the list but
// without the option, those messages get the validator's 401 once the token
// expires, and the vMCP logs a warning the first time a call-bound tool call
// starts.
//
// Build a Binding with New only: its zero value is not usable.
type Binding struct {
	required        map[string]struct{}
	maxCallDuration time.Duration
	record          Record
	recordShared    bool
	challenge       auth.BearerChallenge
	// admissions maps each ExpiredTokenAdmission's issuer and client to its
	// grace. Empty means exp is strict.
	admissions map[admissionKey]time.Duration

	// returned holds the calls whose request has returned on this replica but
	// that the record could not mark as ended.
	returned returnedCalls
	// dispatches holds the calls whose dispatch is running on this replica.
	dispatches dispatches
	// validatorWired reports that TokenValidatorOption has been called.
	validatorWired atomic.Bool
}

// endTimeout bounds marking a call as ended in the record. The request context
// may already be cancelled when the call ends, so the record gets a context of
// its own, as the session manager gives its storage.
const endTimeout = 5 * time.Second

// New validates cfg and returns the Binding it describes. It returns an error
// wrapping vmcp.ErrInvalidConfig for a negative MaxCallDuration, an empty
// client ID, or an ExpiredTokenAdmission list that
// ValidateExpiredTokenAdmission refuses.
func New(cfg Config) (*Binding, error) {
	if cfg.MaxCallDuration < 0 {
		return nil, fmt.Errorf("%w: CallBinding.MaxCallDuration must not be negative", vmcp.ErrInvalidConfig)
	}
	if slices.Contains(cfg.Clients, "") {
		return nil, fmt.Errorf("%w: CallBinding.Clients must not contain an empty client ID", vmcp.ErrInvalidConfig)
	}
	maxCallDuration := cmp.Or(cfg.MaxCallDuration, DefaultMaxCallDuration)
	if err := ValidateExpiredTokenAdmission(cfg.ExpiredTokenAdmission, maxCallDuration); err != nil {
		return nil, fmt.Errorf("%w: CallBinding.ExpiredTokenAdmission: %w", vmcp.ErrInvalidConfig, err)
	}
	admissions := make(map[admissionKey]time.Duration, len(cfg.ExpiredTokenAdmission))
	for _, e := range cfg.ExpiredTokenAdmission {
		admissions[admissionKey{issuer: e.Issuer, clientID: e.ClientID}] = e.Grace
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
		maxCallDuration: maxCallDuration,
		record:          record,
		recordShared:    cfg.Record != nil,
		challenge:       challenge,
		admissions:      admissions,
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

// OpenCall returns the call recorded under key and reports whether it is open
// at now: recorded, not ended, and younger than the maximum call duration. A
// call is open while its request is, or, on Legacy, while its dispatch still
// runs on this replica (see BeginDispatch).
func (b *Binding) OpenCall(ctx context.Context, key string, now time.Time) (Call, bool, error) {
	call, found, err := b.record.Lookup(ctx, key)
	if err != nil || !found {
		return call, false, err
	}
	open := !call.Ended && !b.returned.has(key, now) && now.Before(call.Started.Add(b.maxCallDuration))
	return call, open, nil
}

// OnCallSession reports whether r names call's session. A call without a
// session (Modern) has none, so no request names it.
func OnCallSession(r *http.Request, call Call) bool {
	sessionID, err := sessionbinding.RequestID(r, "")
	return err == nil && call.SessionID != "" && sessionID == call.SessionID
}

// EndCall ends the call token started, once the request that started it has
// returned, until expiry: the record's expiry of the token. When a dispatch of
// the call still runs on this replica, as on Legacy after the client closes
// the call's POST, the call stays open and the last dispatch ends it. When the
// record cannot mark the call as ended, this replica remembers it, so it
// refuses the call's messages from then on.
func (b *Binding) EndCall(ctx context.Context, token *Token, expiry time.Time) {
	if b.dispatches.deferEnd(token.Key, expiry) {
		return
	}
	b.endInRecord(ctx, token, expiry)
}

// BeginDispatch marks a dispatch of the call token started as running on this
// replica, and returns the function that marks it done. Legacy dispatch calls
// it: closing a Legacy call's POST returns its request but does not stop its
// dispatch, which a later cancellation must still reach. A dispatch that
// begins after the call has ended does not reopen it.
func (b *Binding) BeginDispatch(token *Token) (done func()) {
	b.dispatches.begin(token.Key)
	var once sync.Once
	return func() {
		once.Do(func() {
			if expiry, end := b.dispatches.finish(token.Key); end {
				b.endInRecord(context.Background(), token, expiry)
			}
		})
	}
}

// endInRecord marks the call token started as ended in the record, with a
// deadline of its own: a stalled store must not hold the caller, and with it
// Shutdown.
func (b *Binding) endInRecord(ctx context.Context, token *Token, expiry time.Time) {
	endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), endTimeout)
	defer cancel()
	if err := b.record.End(endCtx, token.Key); err != nil {
		b.returned.add(token.Key, expiry, time.Now())
		slog.WarnContext(ctx, "call binding: could not mark a call as ended; "+
			"this replica refuses its messages, other replicas follow the record",
			"jti", token.JTI, "error", err)
	}
}

// TokenValidatorOption returns the token-validator option for this Binding.
// Give it to the validator in front of the vMCP that serves this Binding.
// Without an ExpiredTokenAdmission entry, it keeps exp strict
// (auth.WithStrictExpiry). With one, it accepts, after a call-bound token
// expires, the requests AdmitExpiredToken admits; the maximum call duration
// bounds every entry's grace.
func (b *Binding) TokenValidatorOption() auth.TokenValidatorOption {
	b.validatorWired.Store(true)
	if !b.AdmitsExpiredTokens() {
		return auth.WithStrictExpiry()
	}
	return auth.WithExpiredTokenAdmission(b.maxCallDuration, func(r *http.Request, token auth.ExpiredToken) bool {
		return b.AdmitExpiredToken(r, token, time.Now())
	})
}

// ValidatorWired reports whether TokenValidatorOption has been called.
func (b *Binding) ValidatorWired() bool { return b.validatorWired.Load() }

// AdmitExpiredToken reports whether the token validator may accept r, whose
// token passed every check but exp, at now. It admits r only when all of these
// hold:
//   - the token carries an mcp_tool_call entry for a tool call;
//   - its iss and client_id match an ExpiredTokenAdmission entry, and now is
//     less than that entry's grace after exp (see WithinExpiredTokenGrace);
//   - its key has a call in the record, and the call has a session (a Modern
//     call has none);
//   - r names that session;
//   - r is a POST, a GET or a DELETE.
//
// The call need not be open: the vMCP's call-binding check answers a message
// for a call that has ended without dispatching it, as a client that races the
// call's end must not see a 401, which go-sdk clients treat as a broken
// connection.
//
// A list-call token is never admitted: a list call has no messages after exp.
// The vMCP's call-binding check then admits only the open call's own
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
	case token.Entry.IsList():
		return refused("list_call")
	}
	if err := b.withinGrace(expired.Claims, expired.ExpiredAt, now); err != nil {
		return refused(err.Error())
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
	case !found:
		return refused(ReasonNoOpenCall)
	case !OnCallSession(r, call):
		return refused(ReasonSessionMismatch)
	}
	return true
}

// dispatches is the set of calls, keyed by token key, whose dispatch is
// running on this replica, with whether their request has returned meanwhile.
// An entry lasts while a dispatch runs.
type dispatches struct {
	mu    sync.Mutex
	calls map[string]*dispatchState
}

type dispatchState struct {
	running         int
	requestReturned bool
	expiry          time.Time
}

func (d *dispatches) begin(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.calls == nil {
		d.calls = make(map[string]*dispatchState)
	}
	state, ok := d.calls[key]
	if !ok {
		state = &dispatchState{}
		d.calls[key] = state
	}
	state.running++
}

// finish marks one dispatch of key done. When it was the last, it reports
// whether the call's request has returned, so the call must be ended now,
// with the record's expiry.
func (d *dispatches) finish(key string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.calls[key]
	if !ok {
		return time.Time{}, false
	}
	state.running--
	if state.running > 0 {
		return time.Time{}, false
	}
	delete(d.calls, key)
	return state.expiry, state.requestReturned
}

// deferEnd reports whether a dispatch of key is running, and if so records
// that the call's request has returned, so the last dispatch ends the call.
func (d *dispatches) deferEnd(key string, expiry time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	state, ok := d.calls[key]
	if !ok || state.running == 0 {
		return false
	}
	state.requestReturned = true
	state.expiry = expiry
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
