// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnreadableCharset checks which Content-Type values declare a charset
// the filter cannot read a body in.
func TestUnreadableCharset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		contentTypes []string
		expected     string
	}{
		{name: "no content type"},
		{name: "no charset", contentTypes: []string{"application/json"}},
		{name: "utf-8", contentTypes: []string{"text/event-stream; charset=utf-8"}},
		{name: "upper-case utf-8", contentTypes: []string{"application/json; charset=UTF-8"}},
		{name: "quoted utf-8", contentTypes: []string{`application/json; charset="utf-8"`}},
		{name: "utf8", contentTypes: []string{"application/json; charset=utf8"}},
		{name: "us-ascii", contentTypes: []string{"application/json; charset=us-ascii"}},
		{name: "ascii", contentTypes: []string{"application/json; charset=ascii"}},
		{name: "utf-16le", contentTypes: []string{"text/event-stream; charset=utf-16le"}, expected: "utf-16le"},
		{name: "utf-16le without a space", contentTypes: []string{"text/event-stream;charset=utf-16le"}, expected: "utf-16le"},
		{name: "quoted upper-case utf-16le", contentTypes: []string{`text/event-stream; charset="UTF-16LE"`}, expected: "utf-16le"},
		{name: "utf-7", contentTypes: []string{"text/event-stream; charset=utf-7"}, expected: "utf-7"},
		{name: "utf-16le after another parameter", contentTypes: []string{"application/json; x=1; charset=utf-16le"}, expected: "utf-16le"},
		{name: "extended charset parameter", contentTypes: []string{"text/event-stream; charset*=utf-8''utf-16le"}, expected: "utf-16le"},
		{name: "utf-16le in a second value", contentTypes: []string{"application/json", "text/plain; charset=utf-16le"}, expected: "utf-16le"},
		{
			name:         "malformed value naming a charset",
			contentTypes: []string{"text/event-stream; charset="},
			expected:     "text/event-stream; charset=",
		},
		{
			name:         "two charsets in one value",
			contentTypes: []string{"text/event-stream; charset=utf-8; charset=utf-16le"},
			expected:     "text/event-stream; charset=utf-8; charset=utf-16le",
		},
		{name: "malformed value without a charset", contentTypes: []string{"text/event-stream; =x"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			header := http.Header{}
			for _, value := range tt.contentTypes {
				header.Add("Content-Type", value)
			}
			assert.Equal(t, tt.expected, unreadableCharset(header))
		})
	}
}

// TestListToolsFilterWithholdsBodiesInAnotherCharset checks that a body whose
// Content-Type declares a charset the filter does not read, which a client
// that decodes by the declared charset reads differently from the filter, is
// withheld whenever the filter would have had to read it, and answered with
// an error that carries none of its bytes; that a body in UTF-8 or US-ASCII,
// however the charset is spelled, is filtered as before, an event stream
// event by event; that a response declaring such a charset but carrying no
// body is forwarded as it is; and that a non-2xx body of an unsupported type
// passes through unread whatever its charset.
func TestListToolsFilterWithholdsBodiesInAnotherCharset(t *testing.T) {
	t.Parallel()

	list := listPrefix + `{"tools":` + bothTools + `}}`
	filtered := listPrefix + `{"tools":[` + allowedTool + `]}}`
	utf16Event := utf16LE(sseEvent(list))

	tests := []struct {
		name        string
		status      int
		contentType string
		chunks      []string
		// withheld reports that the client is answered with an error;
		// otherwise it receives expected under the backend's status, and
		// afterFlush, when set, is what it has received after each flush.
		withheld   bool
		expected   string
		afterFlush []string
	}{
		{
			name:        "utf-16le event stream",
			status:      http.StatusOK,
			contentType: "text/event-stream; charset=utf-16le",
			chunks:      []string{utf16Event},
			withheld:    true,
		},
		{
			name:        "quoted upper-case utf-16le event stream",
			status:      http.StatusOK,
			contentType: `text/event-stream; charset="UTF-16LE"`,
			chunks:      []string{utf16Event},
			withheld:    true,
		},
		{
			name:        "utf-16le event stream across flushes",
			status:      http.StatusOK,
			contentType: "text/event-stream; charset=utf-16le",
			chunks:      []string{utf16Event[:7], utf16Event[7:]},
			withheld:    true,
		},
		{
			name:        "utf-7 event stream",
			status:      http.StatusOK,
			contentType: "text/event-stream; charset=utf-7",
			chunks:      []string{utf7(sseEvent(list))},
			withheld:    true,
		},
		{
			name:        "utf-16le JSON body with a byte order mark",
			status:      http.StatusOK,
			contentType: "application/json; charset=utf-16le",
			chunks:      []string{"\xff\xfe" + utf16LE(list)},
			withheld:    true,
		},
		{
			name:        "extended charset parameter",
			status:      http.StatusOK,
			contentType: "text/event-stream; charset*=utf-8''utf-16le",
			chunks:      []string{utf16Event},
			withheld:    true,
		},
		{
			name:        "utf-8 event stream",
			status:      http.StatusOK,
			contentType: "text/event-stream; charset=utf-8",
			chunks:      []string{sseEvent(progressNotification), sseEvent(list)},
			expected:    sseEvent(progressNotification) + sseEvent(filtered),
			afterFlush:  []string{sseEvent(progressNotification), sseEvent(progressNotification) + sseEvent(filtered)},
		},
		{
			name:        "upper-case utf-8 event stream",
			status:      http.StatusOK,
			contentType: "text/event-stream; charset=UTF-8",
			chunks:      []string{sseEvent(list)},
			expected:    sseEvent(filtered),
		},
		{
			name:        "utf-8 JSON body",
			status:      http.StatusOK,
			contentType: "application/json; charset=utf-8",
			chunks:      []string{list},
			expected:    filtered + "\n",
		},
		{
			name:        "us-ascii JSON body",
			status:      http.StatusOK,
			contentType: "application/json; charset=us-ascii",
			chunks:      []string{list},
			expected:    filtered + "\n",
		},
		{
			name:        "utf-16le 204 without a body",
			status:      http.StatusNoContent,
			contentType: "application/json; charset=utf-16le",
		},
		{
			name:        "utf-16le text at a non-2xx status",
			status:      http.StatusNotFound,
			contentType: "text/plain; charset=utf-16le",
			chunks:      []string{utf16LE("not found")},
			expected:    utf16LE("not found"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			middleware, err := NewListToolsMappingMiddleware(WithToolsFilter("allowed_tool"))
			require.NoError(t, err)

			recorder := httptest.NewRecorder()
			var afterFlush []string
			backend := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				flusher, ok := w.(http.Flusher)
				require.True(t, ok)
				for _, chunk := range tt.chunks {
					_, writeErr := w.Write([]byte(chunk))
					require.NoError(t, writeErr)
					flusher.Flush()
					afterFlush = append(afterFlush, recorder.Body.String())
				}
			})

			request := httptest.NewRequest(http.MethodPost, "/mcp",
				strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			middleware(backend).ServeHTTP(recorder, request)

			if tt.withheld {
				assertWithheld(t, recorder, false, withheldError)
				return
			}
			response := recorder.Result()
			require.NoError(t, response.Body.Close())
			assert.Equal(t, tt.status, response.StatusCode)
			assert.Equal(t, tt.contentType, response.Header.Get("Content-Type"))
			assert.Equal(t, tt.expected, recorder.Body.String())
			if tt.afterFlush != nil {
				assert.Equal(t, tt.afterFlush, afterFlush)
			}
		})
	}
}

// utf16LE encodes s in UTF-16LE.
func utf16LE(s string) string {
	var encoded []byte
	for _, unit := range utf16.Encode([]rune(s)) {
		encoded = append(encoded, byte(unit), byte(unit>>8))
	}
	return string(encoded)
}

// utf7 encodes s in UTF-7 (RFC 2152), writing every character outside the
// directly encoded set and white space as its own base64 block, so that "{"
// becomes "+AHs-".
func utf7(s string) string {
	const direct = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789'(),-./:? \t\r\n"
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(direct, r) {
			b.WriteRune(r)
			continue
		}
		b.WriteString("+" + base64.RawStdEncoding.EncodeToString([]byte(utf16BE(string(r)))) + "-")
	}
	return b.String()
}

// utf16BE encodes s in UTF-16BE.
func utf16BE(s string) string {
	var encoded []byte
	for _, unit := range utf16.Encode([]rune(s)) {
		encoded = append(encoded, byte(unit>>8), byte(unit))
	}
	return string(encoded)
}
