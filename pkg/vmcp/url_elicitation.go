// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp

import (
	"errors"
	"fmt"
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
type URLElicitation struct {
	elicitationID string
	url           string
	message       string
}

// NewURLElicitation validates and returns a URL-mode elicitation.
//
// elicitationID identifies the elicitation within one answer; the Modern
// renderer uses it as the inputRequests key. message is the human-readable
// reason shown to the user. rawURL must be an absolute https URL with a host
// and no userinfo; http is accepted only for a loopback host (localhost,
// 127.0.0.0/8, or ::1), for local development.
//
// The URL is the one value vMCP shows only to a client that declared URL-mode
// elicitation. It must not carry credentials, PII, or a pre-authenticated
// token: the page it points at verifies who opens it (see
// docs/arch/vmcp-library.md).
func NewURLElicitation(elicitationID, rawURL, message string) (URLElicitation, error) {
	if elicitationID == "" {
		return URLElicitation{}, errors.New("url elicitation: elicitation ID is required")
	}
	if message == "" {
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
//     mode "url" per elicitation.
//   - Any other Modern client receives -32021 naming elicitation.url as the
//     required capability, without the URL.
//   - A Legacy (2025-11-25) session receives an isError tool result whose
//     text is the elicitation message, without the URL.
//
// On resources/read and prompts/get the answer renders as a generic error.
//
// The type deliberately does not implement pkg/mcp.CodedError: -32042 exists
// only in the 2025-11-25 revision, and a CodedError would render it on Modern.
// Error() never includes a URL, so code paths that stringify the error
// (composite workflows, code-mode scripts, logs) cannot leak it.
type URLElicitationRequiredError struct {
	elicitations []URLElicitation
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

// Error names the condition and the elicitation messages. It never includes a
// URL.
func (e *URLElicitationRequiredError) Error() string {
	messages := make([]string, 0, len(e.elicitations))
	for _, el := range e.elicitations {
		messages = append(messages, el.message)
	}
	return "URL elicitation required: " + strings.Join(messages, "; ")
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
