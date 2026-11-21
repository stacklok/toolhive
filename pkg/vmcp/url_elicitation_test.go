// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package vmcp_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

func TestNewURLElicitation_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		url     string
		message string
		wantErr string
	}{
		{name: "https accepted", id: "e1", url: "https://connect.example.com/github?x=1", message: "Reconnect GitHub"},
		{name: "http localhost accepted", id: "e1", url: "http://localhost:8080/connect", message: "m"},
		{name: "http LOCALHOST accepted", id: "e1", url: "http://LOCALHOST/connect", message: "m"},
		{name: "http 127.0.0.1 accepted", id: "e1", url: "http://127.0.0.1:9000/connect", message: "m"},
		{name: "http 127.255.0.9 accepted", id: "e1", url: "http://127.255.0.9/connect", message: "m"},
		{name: "http ::1 accepted", id: "e1", url: "http://[::1]:9000/connect", message: "m"},

		{name: "empty message", id: "e1", url: "https://example.com", message: "", wantErr: "message is required"},
		{name: "whitespace-only message", id: "e1", url: "https://example.com", message: " \n\t", wantErr: "message is required"},
		{name: "empty elicitation ID", id: "", url: "https://example.com", message: "m", wantErr: "elicitation ID is required"},
		{name: "whitespace-only elicitation ID", id: "  ", url: "https://example.com", message: "m", wantErr: "elicitation ID is required"},
		{name: "URL that does not parse", id: "e1", url: "https://exa mple.com/%zz", message: "m", wantErr: "does not parse"},
		{name: "relative URL has no host", id: "e1", url: "/connect", message: "m", wantErr: "must have a host"},
		{name: "empty URL", id: "e1", url: "", message: "m", wantErr: "must have a host"},
		{name: "empty host with port", id: "e1", url: "https://:443/connect", message: "m", wantErr: "must have a host"},
		{name: "userinfo", id: "e1", url: "https://user:pass@example.com/", message: "m", wantErr: "userinfo"},
		{name: "username only", id: "e1", url: "https://user@example.com/", message: "m", wantErr: "userinfo"},
		{name: "javascript scheme", id: "e1", url: "javascript://example.com/%0aalert(1)", message: "m", wantErr: "scheme"},
		{name: "ftp scheme", id: "e1", url: "ftp://example.com/file", message: "m", wantErr: "scheme"},
		{name: "http non-loopback host", id: "e1", url: "http://example.com/connect", message: "m", wantErr: "loopback"},
		{name: "http non-loopback IP", id: "e1", url: "http://10.0.0.1/connect", message: "m", wantErr: "loopback"},
		{name: "http host that merely starts with localhost", id: "e1", url: "http://localhost.example.com/", message: "m", wantErr: "loopback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := vmcp.NewURLElicitation(tt.id, tt.url, tt.message)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				assert.Equal(t, vmcp.URLElicitation{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.id, got.ElicitationID())
			assert.Equal(t, tt.url, got.URL())
			assert.Equal(t, tt.message, got.Message())
		})
	}
}

func TestNewURLElicitationRequiredError_Validation(t *testing.T) {
	t.Parallel()

	a := mustURLElicitation(t, "a", "https://example.com/a", "connect A")
	b := mustURLElicitation(t, "b", "https://example.com/b", "connect B")
	duplicateOfA := mustURLElicitation(t, "a", "https://example.com/other", "connect A again")

	t.Run("zero elicitations rejected", func(t *testing.T) {
		t.Parallel()
		got, err := vmcp.NewURLElicitationRequiredError()
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("duplicate elicitation IDs rejected", func(t *testing.T) {
		t.Parallel()
		got, err := vmcp.NewURLElicitationRequiredError(a, b, duplicateOfA)
		require.Error(t, err)
		assert.Contains(t, err.Error(), `duplicate elicitation ID "a"`)
		assert.Nil(t, got)
	})

	t.Run("zero-value elicitation rejected", func(t *testing.T) {
		t.Parallel()
		got, err := vmcp.NewURLElicitationRequiredError(a, vmcp.URLElicitation{})
		require.Error(t, err)
		assert.Nil(t, got)
	})

	t.Run("distinct elicitations accepted in order", func(t *testing.T) {
		t.Parallel()
		got, err := vmcp.NewURLElicitationRequiredError(a, b)
		require.NoError(t, err)
		assert.Equal(t, []vmcp.URLElicitation{a, b}, got.Elicitations())
	})

	t.Run("Elicitations returns a copy", func(t *testing.T) {
		t.Parallel()
		input := []vmcp.URLElicitation{a, b}
		got, err := vmcp.NewURLElicitationRequiredError(input...)
		require.NoError(t, err)

		input[0] = b // the caller's slice must not alias the answer
		out := got.Elicitations()
		out[1] = a // nor may the accessor's result
		assert.Equal(t, []vmcp.URLElicitation{a, b}, got.Elicitations())
	})
}

func TestURLElicitationFromError(t *testing.T) {
	t.Parallel()

	answer := mustURLAnswer(t)

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "direct", err: answer, want: true},
		{name: "wrapped once", err: fmt.Errorf("backend call: %w", answer), want: true},
		{name: "wrapped twice", err: fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", answer)), want: true},
		{name: "joined", err: errors.Join(errors.New("other"), answer), want: true},
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: errors.New("URL elicitation required: connect"), want: false},
		{name: "authorization failure", err: vmcp.ErrAuthorizationFailed, want: false},
		{name: "input required error", err: &vmcp.InputRequiredError{Sentinel: errors.New("x")}, want: false},
		{name: "zero-value literal", err: &vmcp.URLElicitationRequiredError{}, want: false},
		{name: "%v does not wrap", err: fmt.Errorf("flattened: %v", answer), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := vmcp.URLElicitationFromError(tt.err)
			assert.Equal(t, tt.want, ok)
			if tt.want {
				assert.Same(t, answer, got)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestURLElicitationRequiredError_ErrorOmitsURL(t *testing.T) {
	t.Parallel()

	a := mustURLElicitation(t, "a", "https://connect.example.com/a?state=secret-a", "Reconnect GitHub")
	b := mustURLElicitation(t, "b", "http://127.0.0.1:9000/b", "Reconnect Jira")
	answer, err := vmcp.NewURLElicitationRequiredError(a, b)
	require.NoError(t, err)

	msg := answer.Error()
	assert.Contains(t, msg, "URL elicitation required")
	assert.Contains(t, msg, "Reconnect GitHub")
	assert.Contains(t, msg, "Reconnect Jira")
	for _, leak := range []string{"connect.example.com", "secret-a", "127.0.0.1", "https://", "http://"} {
		assert.NotContains(t, msg, leak)
	}
	// Stringifying through a wrapper, as composite workflows and logs do, is
	// equally clean.
	assert.NotContains(t, fmt.Sprintf("step failed: %v", answer), "connect.example.com")
}

func TestURLElicitationRequiredError_WithLegacyFallback(t *testing.T) {
	t.Parallel()

	const fallbackHost = "console.example.com"
	const fallback = "Reconnect GitHub at https://" + fallbackHost + "/github/connect, then retry."
	a := mustURLElicitation(t, "a", "https://connect.example.com/a?state=secret-a", "Reconnect GitHub")
	b := mustURLElicitation(t, "b", "https://connect.example.com/b", "Reconnect Jira")
	answer, err := vmcp.NewURLElicitationRequiredError(a, b)
	require.NoError(t, err)

	t.Run("default is the elicitation messages", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, answer.Message(), answer.LegacyFallback())
		assert.Equal(t, "Reconnect GitHub; Reconnect Jira", answer.LegacyFallback())
	})

	for name, text := range map[string]string{"empty": "", "whitespace": " \t\n"} {
		t.Run(name+" text rejected", func(t *testing.T) {
			t.Parallel()
			got, err := answer.WithLegacyFallback(text)
			require.Error(t, err)
			assert.Nil(t, got)
		})
	}

	t.Run("returns a copy carrying the text", func(t *testing.T) {
		t.Parallel()
		got, err := answer.WithLegacyFallback(fallback)
		require.NoError(t, err)
		assert.NotSame(t, answer, got)
		assert.Equal(t, fallback, got.LegacyFallback())
		assert.Equal(t, []vmcp.URLElicitation{a, b}, got.Elicitations())
		assert.Equal(t, answer.Message(), got.Message(), "Message stays the elicitation messages")
		assert.Equal(t, "Reconnect GitHub; Reconnect Jira", answer.LegacyFallback(), "the receiver is unchanged")

		extracted, ok := vmcp.URLElicitationFromError(fmt.Errorf("backend: %w", got))
		require.True(t, ok)
		assert.Same(t, got, extracted)
	})

	t.Run("Error and fmt never print the fallback", func(t *testing.T) {
		t.Parallel()
		got, err := answer.WithLegacyFallback(fallback)
		require.NoError(t, err)
		for _, out := range []string{
			got.Error(),
			fmt.Sprintf("%v", got),
			fmt.Sprintf("%+v", got),
			fmt.Sprintf("%#v", got),
			fmt.Sprintf("%#v", *got),
			fmt.Sprintf("%s", fmt.Errorf("step failed: %w", got)),
		} {
			assert.NotContains(t, out, fallbackHost)
		}
	})
}

// TestURLElicitation_FormattingOmitsURL pins that no fmt verb and no slog
// handler prints the URL of an elicitation, of the Elicitations() slice, or of
// the answer itself.
func TestURLElicitation_FormattingOmitsURL(t *testing.T) {
	t.Parallel()

	const secretURL = "https://connect.example.com/a?state=secret-a"
	e := mustURLElicitation(t, "github", secretURL, "Reconnect GitHub")
	answer, err := vmcp.NewURLElicitationRequiredError(e)
	require.NoError(t, err)

	values := map[string]any{
		"elicitation":   e,
		"pointer":       &e,
		"elicitations":  answer.Elicitations(),
		"answer":        answer,
		"answer value":  *answer,
		"wrapped value": struct{ E vmcp.URLElicitation }{E: e},
		"wrapped error": fmt.Errorf("step failed: %w", answer),
	}
	for name, v := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%T %[1]v"} {
			t.Run(name+" "+verb, func(t *testing.T) {
				t.Parallel()
				out := fmt.Sprintf(verb, v)
				assert.NotContains(t, out, "connect.example.com", out)
				assert.NotContains(t, out, "secret-a", out)
			})
		}
	}
	assert.Contains(t, fmt.Sprintf("%v", e), "github")
	assert.Contains(t, fmt.Sprintf("%v", e), "Reconnect GitHub")
	assert.Equal(t, answer.Error(), fmt.Sprintf("%v", answer), "%v of the answer is its Error()")
	assert.Equal(t, answer.Error(), fmt.Sprintf("%d", *answer))

	for name, newHandler := range map[string]func(*bytes.Buffer) slog.Handler{
		"text": func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
		"json": func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
	} {
		t.Run("slog "+name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			slog.New(newHandler(&buf)).Info("answer", "elicitation", e, "elicitations", answer.Elicitations())
			assert.NotContains(t, buf.String(), "connect.example.com", buf.String())
			assert.Contains(t, buf.String(), "github")
		})
	}
}

func mustURLElicitation(t *testing.T, id, rawURL, message string) vmcp.URLElicitation {
	t.Helper()
	e, err := vmcp.NewURLElicitation(id, rawURL, message)
	require.NoError(t, err)
	return e
}

func mustURLAnswer(t *testing.T) *vmcp.URLElicitationRequiredError {
	t.Helper()
	answer, err := vmcp.NewURLElicitationRequiredError(
		mustURLElicitation(t, "e1", "https://connect.example.com/e1", "Reconnect"))
	require.NoError(t, err)
	return answer
}
