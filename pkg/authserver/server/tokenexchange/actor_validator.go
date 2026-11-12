// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package tokenexchange

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/x/errorsx"
)

//go:generate mockgen -destination=mocks/mock_actor_validator.go -package=mocks -source=actor_validator.go ActorTokenValidator

// ActorTokenValidator checks the actor token that a client presents during ID-JAG
// issuance and reports which agent it belongs to. How a validator is registered and
// selected is not part of this interface.
//
// ValidateActor is called once per issuance, after the grant checks and before the
// ID-JAG is signed. ToolHive builds the act claim from the answer. A validator never
// sets claims itself.
//
// # Result
//
// On success, return an ActorResult with a non-empty AgentID and a non-zero
// ExpiresAt. ExpiresAt is the time at which the actor credential stops being valid.
//
// To refuse, return an *ActorDeniedError, optionally wrapped with %w. Use one of
// the reasons ActorDeniedReasonActorTokenExpired, ActorDeniedReasonApprovalRequired or
// ActorDeniedReasonDenied. Return a pointer: an ActorDeniedError value does not
// implement error. A nil *ActorDeniedError returned as the error is a server error,
// not a refusal.
//
// Return a plain error when the validator cannot answer. That includes a validator
// that is not ready yet, for example one bound late at startup. A plain error is
// reported as a server error, never as a denial.
//
// # Requirements
//
//   - Refuse with ActorDeniedReasonActorTokenExpired when the actor credential has expired.
//     If the validator accepts a credential within its own clock-skew allowance, it
//     may return that credential's past expiry in ExpiresAt, as long as it is less
//     than 60 seconds before the time ToolHive checks the result. ToolHive then
//     answers actor_token_expired, so the client renews the credential and retries.
//     An expiry 60 or more seconds in the past is a server error, because it points
//     to a fault in the validator and a client retry would not help.
//   - Refuse with ActorDeniedReasonApprovalRequired when the user has not approved
//     this agent. Use it when asking the user could help. For a validator that
//     decides per target, the approval is for the target in ActorRequest.Target.
//   - Refuse with ActorDeniedReasonDenied for everything else. That includes an agent
//     with no record and a record that is disabled. A denial is final for the client.
//   - Use ActorRequest.Target for decisions that depend on the gateway or connector.
//     It holds the configured target, not text from the request.
//   - Find the user by ActorRequest.Grant.Upstream when the validator binds
//     users by a per-issuer claim. Check that StableClaim is the claim the validator
//     expects for that Issuer, then look the user up by StableValue. A mismatch, or an
//     issuer the validator does not know, is a configuration fault. Return a plain
//     error for it, not a denial, because a denial is final for the client and would
//     hide the fault from operators.
//   - The whole validation, including the validator's own calls to remote services,
//     has a budget of currently 5 seconds; ToolHive may raise it but will not lower
//     it. A validator that calls a remote service should set shorter deadlines of its
//     own.
//   - Do not join a refusal with other errors (errors.Join, or several %w verbs):
//     ToolHive answers a multi-error as a server error. Detection covers errors that
//     implement Unwrap() []error, such as errors.Join and fmt.Errorf with several %w
//     verbs. A multi-error type that exposes its errors through a single-Unwrap
//     chain is not detected, so do not combine a refusal with other errors in any
//     form.
//   - Honor ctx. ToolHive stops waiting when the budget is spent, but a validator
//     that ignores ctx keeps running in its goroutine until it returns.
//   - Never set claims. The issuing code builds act from AgentID and caps the ID-JAG's
//     exp at ExpiresAt.
//   - Return an AgentID that uses only printable ASCII characters from '!' to '~' (no
//     spaces or control characters) and is one of two forms. A plain token is 1 to 255
//     bytes long and contains no ':'. A value that contains ':' must be an absolute
//     URI per RFC 3986 without userinfo or fragment, of up to 2048 bytes, such as a
//     SPIFFE ID, because RFC 7519 requires a StringOrURI value with ':' to be a URI.
//     ToolHive rejects any other value as a server error and never rewrites it. The
//     AgentID must be unique within the validator's namespace; use a URI when IDs
//     must be unique across validators.
//   - Parse Grant.SubjectIdentifier as RFC 9493 JSON. Never compare it as text.
//   - Use ActorResult.CredentialSubject for audit only. It never enters a token.
//   - Keep credentials out of error text.
//   - Be safe for concurrent use.
//
// # Stability
//
// This API is experimental. It is ready to use, but it may change in a minor
// release while ToolHive gains its first embedders. ToolHive lists each change in
// the release notes. Build ActorResult and ActorDeniedError with keyed literals so
// that added fields do not break your code.
//
// # Other rules
//
//   - A reason outside the documented set is a server error, so a validator cannot
//     break clients by returning a value they do not know.
//   - Composition belongs to the embedder. A client uses one validator, and
//     ToolHive offers no chain of validators. An embedder with several checks wraps
//     them in one validator. A wrapper passes the request through unchanged and returns
//     the first refusal.
//   - Not promised: how many times the validator is called for each tool call, the
//     order of this check relative to other server checks beyond what is stated above,
//     or that the validator sees requests that ToolHive refuses earlier.
type ActorTokenValidator interface {
	ValidateActor(ctx context.Context, req ActorRequest) (ActorResult, error)
}

// ActorRequest is the input to ActorTokenValidator.ValidateActor.
type ActorRequest struct {
	// ActorToken is the raw actor credential presented by the client. It is always set
	// when the validator is called. Do not log it or put it in an error.
	ActorToken string
	// ActorTokenType is the actor_token_type parameter. The caller guarantees it is a
	// type the client is allowed to present.
	ActorTokenType string
	// ClientID is the authenticated client that asked for the ID-JAG. It is always set.
	ClientID string
	// Grant describes the user the ID-JAG is issued for.
	Grant GrantOwner
	// Target describes the gateway the ID-JAG is for. It is always set when the
	// validator is called. A validator that does not decide by target may ignore it.
	Target ActorTarget
}

// GrantOwner identifies the user that a grant belongs to.
type GrantOwner struct {
	// Subject is the user's sub claim as ToolHive knows it. It is always set.
	Subject string
	// SubjectIdentifier is the grant's sub_id as the raw RFC 9493 JSON text. Parse it;
	// do not compare it as text. The zero value means the grant has none.
	SubjectIdentifier string
	// Upstream is the user's identity at the upstream provider. It is the zero value
	// when the server does not record upstream identities (see
	// authserver.Config.SubjectIdentifierAudiences). A validator that needs it returns a plain error
	// when it is missing, not a denial. ToolHive passes either a complete identity,
	// with all four members set, or the zero value.
	Upstream UpstreamIdentity
}

// UpstreamIdentity is a user's identity at the upstream provider.
type UpstreamIdentity struct {
	// Issuer is the upstream's issuer URL.
	Issuer string
	// Subject is the upstream's raw sub from its validated ID token.
	Subject string
	// StableClaim names the upstream claim ToolHive keys users on, for example
	// "uid" or "oid", or "sub".
	StableClaim string
	// StableValue is that claim's value for this user. A validator can bind users by
	// a claim that is specific to one issuer.
	StableValue string
}

// ActorTarget describes the gateway that the ID-JAG is issued for.
type ActorTarget struct {
	// Issuer is the issuer of the target authorization server as configured. It
	// becomes the ID-JAG's aud claim. It is always set when the validator is called.
	Issuer string
	// Resource is the resource the client requested, which is one of the target's
	// configured resources. It is always set.
	Resource string
}

// ActorResult is a validator's answer for an actor that was accepted.
type ActorResult struct {
	// AgentID names the agent instance. ToolHive places it in the act claim as sub. It
	// must follow the rules in ActorTokenValidator. The zero value is a server error.
	AgentID string
	// ExpiresAt is when the actor credential stops being valid. The issuing code caps
	// the ID-JAG's lifetime at this time. Times are compared in whole seconds. A value
	// at or before the time ToolHive checks the result, but less than 60 seconds
	// before it, is answered as actor_token_expired. A value 60 or more seconds before
	// it is a server error, as is the zero value.
	ExpiresAt time.Time
	// CredentialSubject identifies the credential for audit logs. It never enters a
	// token. The zero value means no subject is recorded.
	CredentialSubject string
}

// ActorDeniedReason is the client-facing reason for refusing an actor.
type ActorDeniedReason string

const (
	// ActorDeniedReasonActorTokenExpired means the actor credential has expired. The client
	// should renew the credential and retry.
	ActorDeniedReasonActorTokenExpired ActorDeniedReason = "actor_token_expired" //nolint:gosec // G101: not a credential
	// ActorDeniedReasonApprovalRequired means the user has not approved this agent.
	// Asking the user may help. Retrying alone will not.
	ActorDeniedReasonApprovalRequired ActorDeniedReason = "approval_required"
	// ActorDeniedReasonDenied means the agent is refused and retrying will not help.
	ActorDeniedReasonDenied ActorDeniedReason = "denied"
)

// ActorDeniedError is returned by a validator to refuse an actor. Build it with a
// keyed literal. The zero value has an empty Reason, which ToolHive reports as a
// server error, because the reason is not one of the documented values.
type ActorDeniedError struct {
	// Reason is the client-facing reason. It must be one of the ActorDeniedReason
	// constants. Any other value, including the zero value, is a server error.
	Reason ActorDeniedReason

	// cause is the validator's own error text with the actor token redacted. Only
	// normalizeActorError sets it, so the caller can log why the validator refused.
	cause string
}

// Error returns "actor denied: <reason>", or "actor denied" when Reason is empty.
// It is safe to call on a nil receiver, which also returns "actor denied".
func (e *ActorDeniedError) Error() string {
	if e == nil || e.Reason == "" {
		return "actor denied"
	}
	return "actor denied: " + string(e.Reason)
}

// Reasons logged for an actor validation outcome. They are fixed strings. The
// three refusal reasons reuse the ActorDeniedReason values the client sees.
const (
	refuseActorValidator = "actor_validator_error"
	refuseActorResult    = "actor_result_invalid"
	refuseActorCanceled  = "actor_validator_canceled"
	refuseActorTimeout   = "actor_validator_timeout"
	refuseActorPanic     = "actor_validator_panic"
)

// actorValidationTimeout is the fixed budget for one validation, including the
// validator's own calls to remote services.
const actorValidationTimeout = 5 * time.Second

// defaultActorValidatorCapacity is the default number of validator calls that may
// run at once. Each one holds a goroutine, the raw actor token and whatever the
// validator holds open, and a call that outlives its budget keeps all of that until
// the validator returns. A healthy validator finishes well inside the 5 second
// budget, so 64 calls in flight allow about 600 validations a second at 100ms each,
// far above what one server issues, while a validator that hangs cannot hold more
// than 64 tokens.
const defaultActorValidatorCapacity = 64

// redactedActorToken replaces the actor token in text that reaches a log or an error.
const redactedActorToken = "[redacted]"

// actorExpirySkew is how far in the past a validator may report an actor
// credential's expiry and still be answered as an expired credential. An older
// expiry, such as the Unix epoch, is a fault in the validator, and a client that
// retried on it would retry forever.
const actorExpirySkew = 60 * time.Second

// maxActorAgentIDLen is the longest plain-token AgentID, in bytes, that ToolHive
// accepts. maxActorAgentURILen is the longest AgentID that is an absolute URI.
const (
	maxActorAgentIDLen  = 255
	maxActorAgentURILen = 2048
)

// mapActorOutcome turns a validator's answer into the accepted result, the
// reason to log and the error for the client. err must come from
// normalizeActorError. checkedAt is the time just after the validator returned.
// On any error the returned result is the zero value, and when err is not nil the
// result passed in is ignored.
func mapActorOutcome(res ActorResult, err error, checkedAt time.Time) (ActorResult, string, error) {
	if err != nil {
		reason, clientErr := classifyActorError(err)
		return ActorResult{}, reason, clientErr
	}
	if reason, clientErr := checkActorResult(res, checkedAt); clientErr != nil {
		return ActorResult{}, reason, clientErr
	}
	return res, "", nil
}

// actorRefusal builds the client error for a refusal reason. The client sees only
// the reason. A non-empty cause travels with WithWrap, which fosite does not
// serialize, so the caller can log it.
func actorRefusal(reason ActorDeniedReason, cause string) error {
	refusal := exactReason(fosite.ErrInvalidRequest.ErrorField, string(reason))
	if cause != "" {
		refusal = refusal.WithWrap(errors.New(cause))
	}
	return errorsx.WithStack(refusal)
}

// classifyActorError maps an error from normalizeActorError. Only a non-nil
// *ActorDeniedError with a documented reason is a refusal; everything else is a
// server error.
func classifyActorError(err error) (string, error) {
	var denied *ActorDeniedError
	if !errors.As(err, &denied) || denied == nil {
		return refuseActorValidator, actorValidationFailed(err)
	}
	switch denied.Reason {
	case ActorDeniedReasonActorTokenExpired, ActorDeniedReasonApprovalRequired, ActorDeniedReasonDenied:
		return string(denied.Reason), actorRefusal(denied.Reason, denied.cause)
	default:
		return refuseActorValidator, actorValidationFailed(errors.New(denied.cause))
	}
}

// checkActorResult checks an accepted result: the agent ID, then the expiry.
// Expiry is compared in whole seconds, the resolution of a JWT exp claim. An
// expiry at or before checkedAt is a refusal unless it is actorExpirySkew or more
// in the past, which is a server error.
func checkActorResult(res ActorResult, checkedAt time.Time) (string, error) {
	if problem := agentIDProblem(res.AgentID); problem != nil {
		return refuseActorResult, actorValidationFailed(problem)
	}
	if res.ExpiresAt.IsZero() {
		return refuseActorResult, actorValidationFailed(errors.New("expiry is zero"))
	}
	// Compare without subtracting from ExpiresAt, which wraps for extreme values.
	exp, now := res.ExpiresAt.Unix(), checkedAt.Unix()
	if exp <= now-int64(actorExpirySkew/time.Second) {
		return refuseActorResult, actorValidationFailed(
			fmt.Errorf("expiry is more than %d seconds in the past", int64(actorExpirySkew/time.Second)))
	}
	if exp <= now {
		return string(ActorDeniedReasonActorTokenExpired), actorRefusal(ActorDeniedReasonActorTokenExpired, "")
	}
	return "", nil
}

// agentIDProblem returns nil when id is printable ASCII ('!' to '~') and either
// 1 to 255 bytes with no ':', or up to 2048 bytes that form an absolute URI per
// RFC 3986 (see uriProblem). Otherwise it describes the problem by length, or by
// the offset and value of the first bad byte, never by the ID itself. It checks
// bytes, not runes.
func agentIDProblem(id string) error {
	if id == "" || len(id) > maxActorAgentURILen {
		return fmt.Errorf("agent ID is invalid: length %d", len(id))
	}
	for i := 0; i < len(id); i++ {
		if c := id[i]; c < 0x21 || c > 0x7E {
			return fmt.Errorf("agent ID is invalid: byte 0x%02x at offset %d", c, i)
		}
	}
	if !strings.Contains(id, ":") {
		if len(id) > maxActorAgentIDLen {
			return fmt.Errorf("agent ID is invalid: length %d", len(id))
		}
		return nil
	}
	return uriProblem(id)
}

// uriAllowedPunctuation holds the RFC 3986 characters other than letters, digits
// and '%' that may appear in a URI, without '#', which ToolHive rejects.
const uriAllowedPunctuation = "-._~:/?[]@!$&'()*+,;="

// uriProblem returns nil when id is an absolute URI per RFC 3986 that has no
// userinfo, no fragment and something after the scheme. url.Parse accepts more
// than the RFC allows, so the characters and the percent-escapes are checked
// first. The description never contains id, and the parser's own error text is
// not used because it quotes the input.
func uriProblem(id string) error {
	for i := 0; i < len(id); i++ {
		switch c := id[i]; {
		case c == '#':
			return fmt.Errorf("agent ID is invalid: URI contains '#' at offset %d", i)
		case c == '%':
			if i+2 >= len(id) || !isHexDigit(id[i+1]) || !isHexDigit(id[i+2]) {
				return fmt.Errorf("agent ID is invalid: bad percent-escape at offset %d", i)
			}
			i += 2
		case isASCIILetterOrDigit(c) || strings.IndexByte(uriAllowedPunctuation, c) >= 0:
		default:
			return fmt.Errorf("agent ID is invalid: byte 0x%02x at offset %d is not allowed in a URI", c, i)
		}
	}
	u, err := url.Parse(id)
	if err != nil || u.Scheme == "" {
		return errors.New("agent ID is invalid: contains ':' but is not an absolute URI")
	}
	if u.User != nil {
		return errors.New("agent ID is invalid: URI contains userinfo")
	}
	if u.Opaque == "" && u.Host == "" && u.Path == "" {
		return errors.New("agent ID is invalid: URI has nothing after the scheme")
	}
	return nil
}

func isHexDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func isASCIILetterOrDigit(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
}

// actorValidationFailed is the generic server_error for a validator failure the
// client cannot act on. The cause travels with WithWrap so the caller can log
// it. Unlike issueFailed in refresh_token_handler.go, which logs its cause where
// the failure happens, this helper returns the cause, because the validator runs
// in its own goroutine and the caller logs it once. A validator's error reaches
// here already flattened and redacted by normalizeActorError, so nothing the
// validator wrapped is reachable through errors.Is or errors.As: fosite would
// otherwise react to a wrapped fosite.ErrUnknownRequest. Never use WithDebug
// here: fosite appends the debug text to error_description.
func actorValidationFailed(cause error) error {
	return errorsx.WithStack(
		fosite.ErrServerError.WithHint("The actor token could not be validated.").WithWrap(cause))
}

// actorOutcome is what the validator goroutine reports: its answer, or a
// description of the panic it recovered.
type actorOutcome struct {
	res      ActorResult
	err      error
	panicked bool
	panicMsg string
}

// actorLimiter bounds the validator goroutines that are running at one time. A
// goroutine holds its slot until the validator returns, not until validateActor
// stops waiting for it. Callers share one limiter per server.
type actorLimiter struct {
	slots chan struct{}
}

// newActorLimiter returns a limiter that allows capacity concurrent calls. It
// rejects a capacity below 1, because zero must not mean unlimited.
func newActorLimiter(capacity int) (*actorLimiter, error) {
	if capacity < 1 {
		return nil, fmt.Errorf("actor validator capacity must be at least 1, got %d", capacity)
	}
	return &actorLimiter{slots: make(chan struct{}, capacity)}, nil
}

// acquire waits for a free slot and returns ctx's error if ctx ends first.
func (l *actorLimiter) acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *actorLimiter) release() { <-l.slots }

// validateActor calls v once and returns the accepted result, the time the
// result was checked, the reason to log and the error for the client. It logs
// nothing and caches nothing.
//
// The call has timeout to finish, counted from now and also limited by ctx. A
// validator that ignores its context keeps running in its own goroutine, but
// validateActor stops waiting. Whichever way the wait ends, the context state
// decides: a canceled ctx is actor_validator_canceled, an expired timeout is
// actor_validator_timeout, even when the validator returned an error or an
// answer at the same moment. A parent context whose own deadline expires first
// counts as canceled, because the request ended, not the validator's budget. A
// validator that returns context.DeadlineExceeded from its own shorter deadline,
// before the budget runs out, is actor_validator_error. A panic in the validator,
// or in the text of a panic value or error it returns, is recovered and reported
// as actor_validator_panic.
//
// limiter caps the validator goroutines that run at once, counting each one until
// the validator returns, so a hung validator holds its slot after validateActor
// has stopped waiting. When no slot is free, validateActor waits for one inside
// the same timeout. If the wait ends first, it starts no goroutine, does not call
// the validator, and fails as actor_validator_canceled or actor_validator_timeout
// by the same context rule as a running call. limiter must not be nil.
//
// Every error carries a cause with each occurrence of req.ActorToken replaced by
// "[redacted]"; unwrap the *fosite.RFC6749Error to read it. For a validator's
// error, including a refusal, the cause is the validator's own text. For a
// refusal the client still sees only the reason: fosite does not serialize the
// cause. A refusal that ToolHive derives itself, an expired result, has no cause.
// On any error the result is the zero value. checkedAt
// is clock() read after the validator returned; it is the zero time when the
// call ends in cancellation, timeout or panic.
func validateActor(
	ctx context.Context,
	v ActorTokenValidator,
	limiter *actorLimiter,
	req ActorRequest,
	clock func() time.Time,
	timeout time.Duration,
) (ActorResult, time.Time, string, error) {
	if ctx.Err() != nil {
		return failedActor(refuseActorCanceled, "request canceled while validating the actor")
	}
	vctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := limiter.acquire(vctx); err != nil {
		reason, cause := actorContextFailure(ctx, vctx, timeout)
		return failedActor(reason, cause)
	}

	outCh := make(chan actorOutcome, 1)
	go runActorValidator(vctx, v, req, outCh, limiter.release)

	var out actorOutcome
	select {
	case out = <-outCh:
	case <-vctx.Done():
	}

	if reason, cause := actorContextFailure(ctx, vctx, timeout); reason != "" {
		return failedActor(reason, cause)
	}
	if out.panicked {
		return failedActor(refuseActorPanic, out.panicMsg)
	}

	checkedAt := clock()
	res, reason, err := mapActorOutcome(out.res, out.err, checkedAt)
	if err != nil {
		return ActorResult{}, checkedAt, reason, err
	}
	return res, checkedAt, "", nil
}

// runActorValidator runs the validator and sends one outcome. The channel has
// room for it, so a validator that returns late never blocks. All calls into
// embedder code happen here, under the recover: the validator, and the Error
// and As methods of the error it returns. The error is normalized to values
// built by ToolHive, with the actor token redacted, before it leaves the
// goroutine, and a panic report is redacted the same way. A validator that ends its
// goroutine without returning or panicking (runtime.Goexit) sends nothing, and
// the caller reports it as a timeout.
//
// release frees the caller's limiter slot exactly once, before the outcome is
// sent, so a caller that has the outcome can rely on the slot being free. The
// first deferred call covers Goexit, which sends nothing.
func runActorValidator(
	ctx context.Context, v ActorTokenValidator, req ActorRequest, out chan<- actorOutcome, release func(),
) {
	released := false
	releaseOnce := func() {
		if !released {
			released = true
			release()
		}
	}
	defer releaseOnce()
	defer func() {
		if p := recover(); p != nil {
			msg := panicText(p) + "\n" + string(debug.Stack())
			releaseOnce()
			out <- actorOutcome{panicked: true, panicMsg: redactActorToken(msg, req.ActorToken)}
		}
	}()
	res, err := v.ValidateActor(ctx, req)
	outcome := actorOutcome{res: res, err: normalizeActorError(err, req.ActorToken)}
	releaseOnce()
	out <- outcome
}

// panicText describes a recovered panic value by type and value. A value whose
// own text panics gets a fixed description.
func panicText(p any) (s string) {
	defer func() {
		if recover() != nil {
			s = "validator panicked with an unprintable value"
		}
	}()
	return fmt.Sprintf("validator panicked: %T: %v", p, p)
}

// normalizeActorError replaces a validator's error with a value built by
// ToolHive in which every occurrence of token is redacted: nil stays nil, a
// non-nil *ActorDeniedError becomes a fresh one that keeps the redacted text of
// the whole error as its cause, and anything else becomes a flat text copy. An error that joins several errors,
// anywhere in its chain, is never a refusal, because errors.As would find a
// denial inside it and hide the other errors, such as an outage.
func normalizeActorError(err error, token string) error {
	if err == nil {
		return nil
	}
	text := redactActorToken(err.Error(), token)
	if !hasMultiError(err) {
		var denied *ActorDeniedError
		if errors.As(err, &denied) && denied != nil {
			return &ActorDeniedError{Reason: denied.Reason, cause: text}
		}
	}
	return errors.New(text)
}

// hasMultiError reports whether err, or any error it wraps, implements
// Unwrap() []error, as errors.Join and a fmt.Errorf with several %w verbs do.
func hasMultiError(err error) bool {
	var multi interface{ Unwrap() []error }
	return errors.As(err, &multi)
}

// actorContextFailure reports the reason and cause when the parent context was
// canceled or the validation budget ran out, and "" when neither happened.
func actorContextFailure(parent, derived context.Context, timeout time.Duration) (string, string) {
	if parent.Err() != nil {
		return refuseActorCanceled, "request canceled while validating the actor"
	}
	if derived.Err() != nil {
		return refuseActorTimeout, fmt.Sprintf("validator did not return within %s", timeout)
	}
	return "", ""
}

// failedActor builds the zero-result return for a server error. The cause must
// already be free of the actor token.
func failedActor(reason, cause string) (ActorResult, time.Time, string, error) {
	return ActorResult{}, time.Time{}, reason, actorValidationFailed(errors.New(cause))
}

// redactActorToken replaces every occurrence of the literal token text in s, not
// other encodings of it. An empty token is never replaced, because ReplaceAll
// with an empty pattern inserts the replacement between every character.
func redactActorToken(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, redactedActorToken)
}
