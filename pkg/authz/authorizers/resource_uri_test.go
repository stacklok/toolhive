// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func controlAndSpaceBytes() []byte {
	b := make([]byte, 0, 0x22)
	for c := byte(0x00); c <= 0x20; c++ {
		b = append(b, c)
	}
	return append(b, 0x7F)
}

func TestCheckResourceURIAccepts(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"harness://static/secret",
		"harness://static/a%20b",
		"harness:/x",
		"harness://h",
		"harness://h//x",
		"urn:x:y",
		"mailto:a@b",
		"https://example.com/",
		"https://example.com:8443/a?b=c&d",
		"http://127.0.0.1/x",
		"file:///etc/x",
		"file:///C:/x",
		"file:///a//b",
		"harness://my_svc/x",
		"harness://static/a%2Fb",
		"harness://static/caf%C3%A9",
		"harness://static/x?q=a%3Ab",
		"harness://xn--a/x",
		"harness://STATIC/x",
		"repo://Stacklok/toolhive/contents/README.md",
		"postgres://user@host:5432/db/t/schema",
		"harness://u:p@h/x",
		"http://u:p@h/x",
		"harness://u%40@h/x",
		"http://u!$&'()*+,@h/x",
	} {
		t.Run(uriName(uri), func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, CheckResourceURI(uri))
		})
	}
}

func TestCheckResourceURIRejects(t *testing.T) {
	t.Parallel()

	rejected := map[string][]string{
		"non-ASCII": {"harness://static/café"},
		"bytes outside the allowed set": {
			`harness://static/a"b`, "harness://static/a<b", "harness://static/a>b", `harness://static/a\b`,
			"harness://static/a^b", "harness://static/a`b", "harness://static/a{b", "harness://static/a|b",
			"harness://static/a}b", "harness://static/a[b", "harness://static/a]b", "harness://static/a#b",
		},
		"scheme": {
			"HARNESS://static/secret", "Harness://static/secret", "data", "::", "1abc:x", "/x", "?x",
		},
		"dot segments": {
			"harness://static/./secret", "harness://static/x/../secret", "harness://static/.",
			"harness://static/..", "harness:x/../y", "harness:/.//x",
		},
		"encoded unreserved": {
			"harness://static/%2e%2e/secret", "harness://static/%73ecret", "harness://static/%7e",
			"harness://static/%2E/x",
		},
		"bad percent-encoding": {"harness://static/%zz", "harness://static/%2", "harness://static/%"},
		"lower-case hex":       {"harness://static/x%3a", "harness://static/caf%c3%a9", "harness://u%3a@h/x"},
		"userinfo": {
			"harness://@h/x", "harness://:@h/x", "harness://u:@h/x", "harness://u;x@h/x",
			"harness://u:a:b@h/x", "file://u@h/x", "harness://u@/x", "harness://u=x@h/x",
			"harness://u@v@h/x", "harness://u:p@q@h/x", "http://u@@h/x",
		},
		"port": {
			"harness://static:/x", "harness://static:080/x", "harness://static:99999/x",
			"https://example.com:443/x", "http://example.com:80/x", "harness://h:0/x",
			"harness://static:8a/x", "harness://:80/x",
		},
		"host": {
			"http://EXAMPLE.com/x", "harness://static./x", "harness://a..b/x", "http://ex%41mple.com/x",
			"harness://-a/x", "harness://a-/x", "harness://[::1]/x",
		},
		"numeric host": {"http://0x7f.1/", "http://127.1/", "http://010.0.0.1/", "http://256.1.1.1/"},
		"special scheme shape": {
			"http://example.com", "http:example.com/a", "file:/etc/x", "file:etc/x",
			"file://localhost/etc/x",
		},
		"quote in special query": {"https://example.com/x?a'b", "file:///x?a'b"},
		"file path": {
			"file:////etc/shadow", "file://////etc/shadow", "file://h//x", "file://h/C:/x",
			"file:///C:x", "file:///:etc/x", "file://h:8080/x", "file://:8080/x",
		},
		"punycode in special host": {"http://xn--a/x", "http://xn--bcher-kva.example/x"},
	}
	for reason, uris := range rejected {
		for _, uri := range uris {
			t.Run(reason+"/"+uriName(uri), func(t *testing.T) {
				t.Parallel()
				err := CheckResourceURI(uri)
				assert.ErrorIs(t, err, ErrInvalidResourceURI)
				if reason == "userinfo" {
					assert.Equal(t, ruleUserinfo, ResourceURIRule(err), "misattributed rule")
				}
			})
		}
	}
}

func TestCheckResourceURIRejectsControlCharactersAndSpace(t *testing.T) {
	t.Parallel()

	// One position per byte, rotating through start, middle and end, so every
	// position is exercised without a subtest for each combination.
	positions := []struct {
		name  string
		build func(c string) string
	}{
		{"start", func(c string) string { return c + "harness://static/secret" }},
		{"middle", func(c string) string { return "harness://static/" + c + "secret" }},
		{"end", func(c string) string { return "harness://static/secret" + c }},
	}
	for i, b := range controlAndSpaceBytes() {
		pos := positions[i%len(positions)]
		uri := pos.build(string(rune(b)))
		t.Run(fmt.Sprintf("0x%02X at %s", b, pos.name), func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, CheckResourceURI(uri), ErrInvalidResourceURI)
		})
	}
}

func TestCheckResourceTemplateURI(t *testing.T) {
	t.Parallel()

	for _, uri := range []string{
		"file:///{name}", "harness://{+path}", "HARNESS://static/secret", "harness://static/./secret",
		"harness://static/café", "", "no-scheme",
	} {
		t.Run(uriName(uri), func(t *testing.T) {
			t.Parallel()
			assert.NoError(t, CheckResourceTemplateURI(uri))
		})
	}
	for _, b := range controlAndSpaceBytes() {
		t.Run(fmt.Sprintf("rejects 0x%02X", b), func(t *testing.T) {
			t.Parallel()
			assert.ErrorIs(t, CheckResourceTemplateURI("file:///a"+string(rune(b))+"b"), ErrInvalidResourceURI)
		})
	}
}

func TestCheckResourceURINamesTheFailedRule(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"harness://static/a b":             ruleBytes,
		"harness://static/x%3a":            rulePercentEncoding,
		"HARNESS://static/x":               ruleScheme,
		"harness://static/./x":             ruleDotSegment,
		"harness://@h/x":                   ruleUserinfo,
		"harness://static:080/x":           rulePort,
		"http://EXAMPLE.com/x":             ruleHost,
		"http://127.1/":                    ruleIPv4,
		"http://example.com":               ruleSpecialShape,
		"file:/etc/x":                      ruleFile,
		"https://example.com/x?a'b":        ruleQuery,
		"harness://static/secret\x00":      ruleBytes,
		"harness://static/secret#fragment": ruleBytes,
		"harness://static/%73ecret":        rulePercentEncoding,
		"harness://static/secret?\x7f":     ruleBytes,
		"file://h/C:/x":                    ruleFile,
		"harness://u:a:b@h/x":              ruleUserinfo,
		"harness://static/%2e%2e/secret":   rulePercentEncoding,
		"harness://static/x/../secret":     ruleDotSegment,
		"http://xn--a/x":                   ruleHost,
		"https://example.com:443/x":        rulePort,
		"harness:x/../y":                   ruleDotSegment,
		"harness://static/\xc3\xa9":        ruleBytes,
		"data":                             ruleScheme,
	}
	for uri, rule := range tests {
		t.Run(uriName(uri), func(t *testing.T) {
			t.Parallel()
			err := CheckResourceURI(uri)
			assert.ErrorIs(t, err, ErrInvalidResourceURI)
			assert.Equal(t, rule, ResourceURIRule(err))
			assert.NotContains(t, err.Error(), "harness", "the error must not carry the URI")
		})
	}

	err := CheckResourceTemplateURI("file:///{name} x")
	assert.ErrorIs(t, err, ErrInvalidResourceURI)
	assert.Equal(t, ruleTemplate, ResourceURIRule(err))
	assert.Empty(t, ResourceURIRule(nil))
}

func uriName(uri string) string { return fmt.Sprintf("%q", uri) }
