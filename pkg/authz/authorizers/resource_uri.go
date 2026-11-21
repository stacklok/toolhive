// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package authorizers

import (
	"errors"
	"strings"
)

// ErrInvalidResourceURI reports a resource URI that is not in canonical form.
// This includes URIs with control characters or spaces.
var ErrInvalidResourceURI = errors.New("resource URI is not in canonical form")

// Rule names carried by invalidResourceURIError. They are fixed strings, so an
// error or a log line that names one never contains caller data.
const (
	ruleBytes           = "bytes"
	rulePercentEncoding = "percent_encoding"
	ruleScheme          = "scheme"
	ruleDotSegment      = "dot_segment"
	ruleUserinfo        = "userinfo"
	rulePort            = "port"
	ruleHost            = "host"
	ruleIPv4            = "ipv4"
	ruleSpecialShape    = "special_shape"
	ruleFile            = "file"
	ruleQuery           = "query"
	ruleTemplate        = "template"
)

// invalidResourceURIError is returned by CheckResourceURI and
// CheckResourceTemplateURI. It matches ErrInvalidResourceURI with errors.Is and
// names the rule that refused the URI, so operators can see which part of a
// server's URI to fix. It never contains the URI.
type invalidResourceURIError struct {
	rule string
}

func (e *invalidResourceURIError) Error() string {
	if e.rule == ruleTemplate {
		return "resource template contains a control character or space"
	}
	return ErrInvalidResourceURI.Error() + " (rule: " + e.rule + ")"
}

// Is makes the error match ErrInvalidResourceURI.
func (*invalidResourceURIError) Is(target error) bool { return target == ErrInvalidResourceURI }

// ResourceURIRule returns the rule named by err, or "" if err does not carry
// one.
func ResourceURIRule(err error) string {
	var e *invalidResourceURIError
	if errors.As(err, &e) {
		return e.rule
	}
	return ""
}

// CheckResourceTemplateURI returns an error matching ErrInvalidResourceURI if
// uri contains a byte in 0x00-0x20 or 0x7F. It is the check for a resource
// template reference, which is a template string rather than a URI: SDKs match
// it by exact string, so only bytes that no URL parser treats literally are
// refused.
//
// The error never contains the URI.
func CheckResourceTemplateURI(uri string) error {
	for i := 0; i < len(uri); i++ {
		if c := uri[i]; c <= 0x20 || c == 0x7F {
			return &invalidResourceURIError{rule: ruleTemplate}
		}
	}
	return nil
}

// CheckResourceURI returns an error matching ErrInvalidResourceURI unless uri
// is already in canonical form: what WHATWG URL serialization leaves unchanged,
// plus a few RFC 3986 normalization rules.
//
// Authorization compares the URI as an exact string, but servers built on the
// TypeScript and Python SDKs look a resource up by a normalized form of it (we
// verified that pydantic, which the Python SDK uses, normalizes like WHATWG),
// and reporters showed that the C# SDK normalizes too. A policy that forbids one
// spelling would then not cover its other spellings. Accepting only canonical
// URIs leaves one spelling per resource, so the exact comparison is the whole
// decision. For C# servers this is only a partial fix: the host case of
// non-special schemes, user information, and possibly decoded "!'()*" escapes
// and the default ports of other schemes are not covered, so use an allow-list
// there.
//
// The check works on bytes and does not use net/url, which accepts input that
// WHATWG rewrites and decodes paths. It fails closed: a rule that is stricter
// than the normalizers rejects a legitimate URI, and a rule that is missing
// would be a gap, so every rule below names the rewrite it guards against.
//
// The error names the failed rule and never contains the URI.
func CheckResourceURI(uri string) error {
	if rule := canonicalResourceURIRule(uri); rule != "" {
		return &invalidResourceURIError{rule: rule}
	}
	return nil
}

// canonicalResourceURIRule returns the name of the first rule uri breaks, or ""
// if it is canonical.
func canonicalResourceURIRule(uri string) string {
	if !onlyURIBytes(uri) {
		return ruleBytes
	}
	if !canonicalPercentEncoding(uri) {
		return rulePercentEncoding
	}
	scheme, rest, ok := splitScheme(uri)
	if !ok || !lowercaseScheme(scheme) {
		return ruleScheme
	}
	authority, hasAuthority, path, query, hasQuery := splitHierarchy(rest)
	if !noDotSegments(path) {
		return ruleDotSegment
	}
	special := isSpecialScheme(scheme)
	if hasAuthority {
		if rule := canonicalAuthority(scheme, authority, special); rule != "" {
			return rule
		}
	}
	if hasQuery && !canonicalQuery(query, special) {
		return ruleQuery
	}
	if special {
		return canonicalSpecialShape(scheme, authority, hasAuthority, path)
	}
	return ""
}

// onlyURIBytes allows the RFC 3986 unreserved, sub-delimiter and a few
// gen-delim bytes plus '%'. Fragments, IPv6 hosts, backslashes (WHATWG turns
// them into '/'), spaces, controls and non-ASCII are refused.
func onlyURIBytes(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !isAlnum(c) && strings.IndexByte("-._~!$&'()*+,;=:@/?%", c) < 0 {
			return false
		}
	}
	return true
}

// canonicalPercentEncoding guards against hex-case normalization (%3a to %3A)
// and decoding of unreserved characters (%41 to A, %2e to '.').
func canonicalPercentEncoding(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+2 >= len(s) || !isUpperHex(s[i+1]) || !isUpperHex(s[i+2]) {
			return false
		}
		if isUnreserved(hexValue(s[i+1])<<4 | hexValue(s[i+2])) {
			return false
		}
		i += 2
	}
	return true
}

// splitScheme requires a scheme, as WHATWG and RFC 3986 do for an absolute URI.
func splitScheme(uri string) (scheme, rest string, ok bool) {
	i := strings.IndexAny(uri, ":/?")
	if i <= 0 || uri[i] != ':' {
		return "", "", false
	}
	return uri[:i], uri[i+1:], true
}

// lowercaseScheme guards against scheme lower-casing (HTTP:// to http://).
func lowercaseScheme(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '+' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

// splitHierarchy splits what follows the scheme into authority, path and query
// as the RFC 3986 Appendix B expression does. '#' cannot occur: onlyURIBytes
// refuses it.
func splitHierarchy(rest string) (authority string, hasAuthority bool, path, query string, hasQuery bool) {
	if strings.HasPrefix(rest, "//") {
		rest = rest[2:]
		end := strings.IndexAny(rest, "/?")
		if end < 0 {
			end = len(rest)
		}
		authority, hasAuthority, rest = rest[:end], true, rest[end:]
	}
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		return authority, hasAuthority, rest[:q], rest[q+1:], true
	}
	return authority, hasAuthority, rest, "", false
}

// noDotSegments guards against dot-segment removal ("/./", "/x/../", and the
// trailing "/." and "/..").
func noDotSegments(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// canonicalQuery guards against WHATWG percent-encoding "'" in the query of a
// special-scheme URL.
func canonicalQuery(query string, special bool) bool {
	return !special || strings.IndexByte(query, '\'') < 0
}

func isSpecialScheme(scheme string) bool {
	switch scheme {
	case "http", "https", "ws", "wss", "ftp", "file":
		return true
	}
	return false
}

func defaultPort(scheme string) string {
	switch scheme {
	case "http", "ws":
		return "80"
	case "https", "wss":
		return "443"
	case "ftp":
		return "21"
	}
	return ""
}

// canonicalAuthority returns the rule the authority breaks, or "".
func canonicalAuthority(scheme, authority string, special bool) string {
	hostPort := authority
	// WHATWG splits user information at the last "@".
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		if scheme == "file" || !canonicalUserinfo(authority[:at]) {
			return ruleUserinfo
		}
		hostPort = authority[at+1:]
		if hostPort == "" {
			return ruleUserinfo
		}
	}
	host := hostPort
	if i := strings.LastIndexByte(hostPort, ':'); i >= 0 {
		host = hostPort[:i]
		if host == "" || !canonicalPort(scheme, hostPort[i+1:]) {
			return rulePort
		}
	}
	if host == "" {
		return ""
	}
	return canonicalHost(host, special)
}

// canonicalUserinfo accepts a non-empty user name and an optional non-empty
// password that WHATWG and pydantic leave unchanged. Both encode ";" and "=",
// drop an empty user or password, and treat a second ":" or "@" as data to
// encode, so these are refused. Percent-escapes were validated for the whole
// URI.
func canonicalUserinfo(userinfo string) bool {
	if strings.Count(userinfo, "@") > 0 || strings.Count(userinfo, ":") > 1 {
		return false
	}
	user, password, hasPassword := strings.Cut(userinfo, ":")
	if user == "" || (hasPassword && password == "") {
		return false
	}
	return plainUserinfoPart(user) && plainUserinfoPart(password)
}

func plainUserinfoPart(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isAlnum(s[i]) && strings.IndexByte("-._~!$&'()*+,%", s[i]) < 0 {
			return false
		}
	}
	return true
}

// canonicalPort guards against dropping an empty port, a default port or
// leading zeros, and refuses a port WHATWG rejects as out of range.
func canonicalPort(scheme, port string) bool {
	if port == "" || len(port) > 5 || port[0] == '0' || port == defaultPort(scheme) {
		return false
	}
	n := 0
	for i := 0; i < len(port); i++ {
		if port[i] < '0' || port[i] > '9' {
			return false
		}
		n = n*10 + int(port[i]-'0')
	}
	return n <= 65535
}

// canonicalHost guards against host lower-casing, empty and trailing-dot
// labels, IDNA processing of "xn--" labels and IPv4 number parsing. Hosts of
// non-special schemes are opaque, so they keep their case and need no IDNA
// check. It returns the rule the host breaks, or "".
func canonicalHost(host string, special bool) string {
	labels := strings.Split(host, ".")
	for _, label := range labels {
		if !canonicalLabel(label, special) || (special && strings.HasPrefix(label, "xn--")) {
			return ruleHost
		}
	}
	if !special {
		return ""
	}
	last := labels[len(labels)-1]
	if (allDigits(last) || strings.HasPrefix(last, "0x")) && !canonicalIPv4(labels) {
		return ruleIPv4
	}
	return ""
}

func canonicalLabel(label string, special bool) bool {
	if label == "" || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
		case c >= 'A' && c <= 'Z' && !special:
		default:
			return false
		}
	}
	return true
}

// canonicalIPv4 guards against WHATWG rewriting "127.1", "0x7f.1" and
// "010.0.0.1" into a dotted quad.
func canonicalIPv4(labels []string) bool {
	if len(labels) != 4 {
		return false
	}
	for _, l := range labels {
		if !allDigits(l) || len(l) > 3 || (len(l) > 1 && l[0] == '0') {
			return false
		}
		n := 0
		for i := 0; i < len(l); i++ {
			n = n*10 + int(l[i]-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// canonicalSpecialShape checks what WHATWG requires or rewrites for the
// special schemes. It returns the rule that is broken, or "".
func canonicalSpecialShape(scheme, authority string, hasAuthority bool, path string) string {
	if scheme == "file" {
		if !canonicalFile(authority, hasAuthority, path) {
			return ruleFile
		}
		return ""
	}
	// "http:example.com/a" is rewritten to "http://example.com/a", and an
	// empty path to "/". canonicalAuthority already refuses a port or user
	// information with an empty host, so a non-empty authority has a host.
	if !hasAuthority || authority == "" || path == "" {
		return ruleSpecialShape
	}
	return ""
}

func canonicalFile(authority string, hasAuthority bool, path string) bool {
	// "file:/x" is rewritten to "file:///x"; "file://localhost/x" drops the
	// host; both normalizers throw on a port.
	if !hasAuthority || authority == "localhost" || strings.IndexByte(authority, ':') >= 0 {
		return false
	}
	// A leading "//" in the path is collapsed.
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return false
	}
	first := path[1:]
	if i := strings.IndexByte(first, '/'); i >= 0 {
		first = first[:i]
	}
	if strings.IndexByte(first, ':') < 0 {
		return true
	}
	// A drive letter drops the host and must stand alone as the first segment.
	return len(first) == 2 && isAlpha(first[0]) && first[1] == ':' && authority == ""
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isAlnum(c byte) bool { return isAlpha(c) || c >= '0' && c <= '9' }
func isUpperHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'A' && c <= 'F'
}
func isUnreserved(c byte) bool { return isAlnum(c) || c == '-' || c == '.' || c == '_' || c == '~' }

func hexValue(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'A' + 10
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
