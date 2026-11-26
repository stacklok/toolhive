// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"strings"
)

// URLElicitation is one MCP URL-mode elicitation: a request that the client
// send the user to URL (for example a connect page that re-establishes one
// backend's credential) and then retry the call.
//
// The fields are unexported so a value can only come from NewURLElicitation,
// which validates them. The zero value is not a valid elicitation, and
// NewURLElicitationRequiredError rejects it.
//
// Format, String, GoString, and LogValue omit the URL, so formatting the
// value with the standard fmt verbs or logging it through slog does not print
// it. Only URL() returns it. Two paths bypass Format and are outside this
// guarantee: %p on a non-pointer (a go vet error), and a copy held in an
// unexported field of another type, which fmt reflects over without calling
// its methods.
type URLElicitation struct {
	elicitationID string
	url           string
	message       string
}

// NewURLElicitation validates and returns a URL-mode elicitation.
//
// elicitationID identifies the elicitation within one answer; the Modern
// renderer uses it as the inputRequests key. message is the human-readable
// reason shown to the user. Both must contain a non-whitespace character.
// rawURL must be an absolute https URL with a host and no userinfo; http is
// accepted only for a loopback host (localhost, 127.0.0.0/8, or ::1), for
// local development.
//
// The URL is the one value vMCP shows only to a client that declared URL-mode
// elicitation. It must not carry credentials, PII, or a pre-authenticated
// token: the page it points at verifies who opens it (see
// docs/arch/vmcp-library.md).
func NewURLElicitation(elicitationID, rawURL, message string) (URLElicitation, error) {
	if strings.TrimSpace(elicitationID) == "" {
		return URLElicitation{}, errors.New("url elicitation: elicitation ID is required")
	}
	if strings.TrimSpace(message) == "" {
		return URLElicitation{}, errors.New("url elicitation: message is required")
	}
	if err := validateElicitationURL(rawURL); err != nil {
		return URLElicitation{}, fmt.Errorf("url elicitation: %w", err)
	}
	return URLElicitation{elicitationID: elicitationID, url: rawURL, message: message}, nil
}

// ElicitationID returns the identifier of the elicitation within its answer.
func (e URLElicitation) ElicitationID() string { return e.elicitationID }

// URL returns the validated URL the client should send the user to.
func (e URLElicitation) URL() string { return e.url }

// Message returns the human-readable reason shown to the user.
func (e URLElicitation) Message() string { return e.message }

// String describes the elicitation by ID and message. It never includes the
// URL.
func (e URLElicitation) String() string {
	return fmt.Sprintf("URLElicitation{id: %q, message: %q}", e.elicitationID, e.message)
}

// GoString is the %#v rendering Format delegates to: the same text as
// String.
func (e URLElicitation) GoString() string { return e.String() }

// Format implements fmt.Formatter and is the redaction entry point for fmt:
// %#v prints GoString, and every other verb prints String.
func (e URLElicitation) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, e.GoString())
		return
	}
	_, _ = io.WriteString(f, e.String())
}

// LogValue logs the elicitation as its ID and message. It never includes the
// URL.
func (e URLElicitation) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("elicitation_id", e.elicitationID),
		slog.String("message", e.message),
	)
}

// URLElicitationRequiredError is the URL elicitation answer: the typed error a
// tools/call implementation returns to ask the client to send the user to one
// or more URLs and then retry the call.
//
// Only Go code running in the vMCP process can produce it, typically a
// BackendClient or VMCP decorator refusing a call (a refusal is the one thing
// the VMCP contract lets a decorator do). Errors from backends never decode
// into this type.
//
// vMCP renders it per negotiated protocol at the tools/call render seam:
//   - A Modern (2026-07-28) client that declared elicitation.url receives an
//     InputRequiredResult carrying one elicitation/create request with
//     mode "url" per elicitation. On a retry that declined or cancelled one
//     of those elicitations, it receives an isError tool result carrying
//     Message(), without the URL, instead of being asked again.
//   - Any other Modern client receives -32021 naming elicitation.url as the
//     required capability, with Message() and without the URL.
//   - A Legacy (2025-11-25) session whose client declared elicitation.url
//     receives -32042 listing the elicitations, when this instance saw the
//     session's initialize. Any other Legacy session, including a rehydrated
//     one, receives an isError tool result whose text is LegacyFallback():
//     Message(), without the URL, unless the embedder set its own text with
//     WithLegacyFallback.
//
// On resources/read and prompts/get the answer renders as a generic error.
//
// The type deliberately does not implement pkg/mcp.CodedError: -32042 exists
// only in the 2025-11-25 revision, and a CodedError would render it on Modern
// and as an isError result on Legacy.
// Error() never includes a URL, so code paths that stringify the error
// (composite workflows, code-mode scripts, logs) cannot leak it. Format keeps
// the URL out of fmt output too, on the pointer and on a dereferenced value,
// within the limits the URLElicitation comment states.
type URLElicitationRequiredError struct {
	elicitations []URLElicitation
	// legacyFallback is the embedder's text for the Legacy isError fallback,
	// set by WithLegacyFallback. Empty means Message().
	legacyFallback string
}

// NewURLElicitationRequiredError builds the URL elicitation answer from one or
// more elicitations built by NewURLElicitation. It rejects an empty list, a
// zero-value URLElicitation, and duplicate elicitation IDs.
func NewURLElicitationRequiredError(elicitations ...URLElicitation) (*URLElicitationRequiredError, error) {
	if len(elicitations) == 0 {
		return nil, errors.New("url elicitation required: at least one elicitation is required")
	}
	seen := make(map[string]struct{}, len(elicitations))
	for i, e := range elicitations {
		// NewURLElicitation never returns an empty ID, so an empty one means
		// the value did not come from the constructor.
		if e.elicitationID == "" {
			return nil, fmt.Errorf(
				"url elicitation required: elicitation %d was not built by NewURLElicitation", i)
		}
		if _, dup := seen[e.elicitationID]; dup {
			return nil, fmt.Errorf("url elicitation required: duplicate elicitation ID %q", e.elicitationID)
		}
		seen[e.elicitationID] = struct{}{}
	}
	return &URLElicitationRequiredError{elicitations: slices.Clone(elicitations)}, nil
}

// Elicitations returns a copy of the answer's elicitations, in the order given
// to NewURLElicitationRequiredError.
func (e *URLElicitationRequiredError) Elicitations() []URLElicitation {
	return slices.Clone(e.elicitations)
}

// Message returns the elicitation messages joined by "; ", in order. It is
// the text every fallback shows by default (the Legacy isError text, the
// Modern -32021 message and decline result) and the text Error embeds. It
// never includes a URL. WithLegacyFallback can replace the Legacy isError
// text; Message itself never changes.
func (e *URLElicitationRequiredError) Message() string {
	messages := make([]string, 0, len(e.elicitations))
	for _, el := range e.elicitations {
		messages = append(messages, el.message)
	}
	return strings.Join(messages, "; ")
}

// WithLegacyFallback returns a copy of the answer whose Legacy isError
// fallback renders text in place of Message(). The fallback is what a Legacy
// session gets when it cannot receive -32042: its client did not declare
// elicitation.url, it negotiated an older revision, or its capabilities are
// unknown, as on a rehydrated session. The receiver is unchanged. text must
// contain a non-whitespace character.
//
// An embedder sets it when a client without URL-mode elicitation still needs
// a way to reach the page, for example a reconnect link written into the
// text. vMCP renders text as given, so an embedder that puts a URL in it
// accepts that a model reading the result sees the URL and can act on it
// without the user's consent step that URL mode provides.
//
// The fallback does not change any other rendering. Error(), Format and
// GoString never include it, so code that stringifies the error does not
// print a URL written into it, and Modern clients keep the renderings the
// type comment lists: a Modern client without elicitation.url gets -32021,
// which tells it to declare the capability, and a Modern decline result
// must not hand back the link the user just declined.
func (e *URLElicitationRequiredError) WithLegacyFallback(text string) (*URLElicitationRequiredError, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("url elicitation required: legacy fallback text is required")
	}
	c := *e
	c.elicitations = slices.Clone(e.elicitations)
	c.legacyFallback = text
	return &c, nil
}

// LegacyFallback returns the text of the Legacy isError fallback: the text
// set by WithLegacyFallback, or Message() when none was set.
func (e *URLElicitationRequiredError) LegacyFallback() string {
	if e.legacyFallback != "" {
		return e.legacyFallback
	}
	return e.Message()
}

// Error names the condition and the elicitation messages. It never includes a
// URL.
func (e *URLElicitationRequiredError) Error() string {
	return "URL elicitation required: " + e.Message()
}

// GoString is the %#v rendering Format delegates to. It renders each
// elicitation through its own GoString, never the URL.
func (e *URLElicitationRequiredError) GoString() string {
	return fmt.Sprintf("&vmcp.URLElicitationRequiredError{elicitations: %#v}", e.elicitations)
}

// Format implements fmt.Formatter on the value receiver, so it is in the
// method set of both the pointer and a dereferenced value. Without it, a
// dereferenced answer has no Error or GoString, and fmt would reflect over
// the unexported slice and print every URL. %#v prints GoString; every other
// verb prints Error.
func (e URLElicitationRequiredError) Format(f fmt.State, verb rune) {
	if verb == 'v' && f.Flag('#') {
		_, _ = io.WriteString(f, e.GoString())
		return
	}
	_, _ = io.WriteString(f, e.Error())
}

// URLElicitationFromError extracts the URL elicitation answer from an error
// chain, through any %w wrapping. It reports false for any other error, and
// for an answer that carries no elicitations (a zero-value struct literal),
// so the render seam only ever sees a validated answer.
func URLElicitationFromError(err error) (*URLElicitationRequiredError, bool) {
	var answer *URLElicitationRequiredError
	if errors.As(err, &answer) && answer != nil && len(answer.elicitations) > 0 {
		return answer, true
	}
	return nil, false
}

// validateElicitationURL accepts an absolute https URL with a host and no
// userinfo, or http on a loopback host.
func validateElicitationURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("URL does not parse")
	}
	// url.Parse succeeds on relative references and opaque forms; check the
	// parts a connect URL actually needs.
	if u.Host == "" || u.Hostname() == "" {
		return errors.New("URL must have a host")
	}
	if u.User != nil {
		return errors.New("URL must not carry userinfo")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return errors.New("http URL is allowed only on a loopback host")
	default:
		return fmt.Errorf("URL scheme %q is not allowed; use https", u.Scheme)
	}
}

// isLoopbackHost reports whether host is localhost, in 127.0.0.0/8, or ::1.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
