// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	// healthTimeout is the maximum time to wait for a health check response.
	healthTimeout = 5 * time.Second

	// NonceHeader is the HTTP header used to return the server nonce.
	NonceHeader = "X-Toolhive-Nonce"
)

// NamedPipePrefix is the Windows named-pipe namespace prefix. The discovery
// dialer uses it to reconstruct addresses for winio.DialPipeContext, and the
// API listener (pkg/api) imports it as the canonical definition so the two
// sides cannot drift.
const NamedPipePrefix = `\\.\pipe\`

// maxNamedPipeNameLen is the Windows limit on the part of a pipe path after
// \\.\pipe\: the underlying CreateNamedPipeW lpName cannot exceed 256 chars
// total, leaving 247 for the name once the prefix is accounted for.
const maxNamedPipeNameLen = 247

// namedPipeNamePattern is the positive charset accepted for pipe names. The
// Windows pipe namespace technically allows more, but restricting to this
// subset keeps round-tripping through net/url predictable and rules out
// shell-meaningful characters that should never appear in our addresses.
var namedPipeNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// reservedNamedPipeNames is the set of legacy Windows device names that
// CreateNamedPipeW will not accept as a pipe name. The check is on the
// already-lowercased hostname.
var reservedNamedPipeNames = map[string]struct{}{
	"con": {}, "nul": {}, "prn": {}, "aux": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {},
	"com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
	"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

// CheckHealth verifies that a server at the given URL is healthy and optionally
// matches the expected nonce. It supports http://, unix://, and npipe:// URL
// schemes (npipe:// only resolves on Windows).
func CheckHealth(ctx context.Context, serverURL string, expectedNonce string) error {
	client, requestURL, err := buildHealthClient(serverURL)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create health request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected health status: %d", resp.StatusCode)
	}

	if expectedNonce != "" {
		actualNonce := resp.Header.Get(NonceHeader)
		if subtle.ConstantTimeCompare([]byte(actualNonce), []byte(expectedNonce)) != 1 {
			return fmt.Errorf("nonce mismatch: expected %q, got %q", expectedNonce, actualNonce)
		}
	}

	return nil
}

// buildHealthClient returns an HTTP client and request URL appropriate for
// the given server URL scheme.
func buildHealthClient(serverURL string) (*http.Client, string, error) {
	client, baseURL, err := HTTPClientForURL(serverURL)
	if err != nil {
		return nil, "", err
	}
	healthURL, err := url.JoinPath(baseURL, "health")
	if err != nil {
		return nil, "", fmt.Errorf("failed to build health URL: %w", err)
	}
	return client, healthURL, nil
}

// HTTPClientForURL returns an HTTP client configured for the given server URL
// and the base URL to use for requests. For unix:// URLs it creates a client
// with a Unix socket transport and returns "http://localhost" as the base URL.
// For npipe:// URLs (Windows only) it dials the named pipe via the platform
// dialNamedPipe helper. For http:// URLs it validates the host is a loopback
// address and returns a default client. The returned client has no timeout
// set; callers should apply their own timeout via context or client.Timeout.
//
// Scheme matching is case-insensitive because url.Parse lowercases the scheme
// during parsing, so npipe://x and NPIPE://x route through the same arm.
func HTTPClientForURL(serverURL string) (*http.Client, string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return nil, "", fmt.Errorf("invalid server URL: %w", err)
	}
	switch u.Scheme {
	case "unix":
		socketPath, err := ParseUnixSocketPath(serverURL)
		if err != nil {
			return nil, "", err
		}
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					return net.Dial("unix", socketPath)
				},
			},
		}
		return client, "http://localhost", nil

	case "npipe":
		pipePath, err := ParseNamedPipeURL(serverURL)
		if err != nil {
			return nil, "", err
		}
		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return dialNamedPipe(ctx, pipePath)
				},
			},
		}
		return client, "http://localhost", nil

	case "http":
		if err := ValidateLoopbackURL(serverURL); err != nil {
			return nil, "", err
		}
		return &http.Client{}, serverURL, nil

	default:
		return nil, "", fmt.Errorf("unsupported URL scheme: %s", u.Scheme)
	}
}

// ValidateLoopbackURL checks that an http:// URL points to a loopback address.
func ValidateLoopbackURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Hostname()

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("invalid host in URL: %s", host)
	}
	if !ip.IsLoopback() {
		return fmt.Errorf("refusing health check to non-loopback address: %s", host)
	}
	return nil
}

// ParseUnixSocketPath extracts and validates the socket path from a unix:// URL.
// The expected form is unix:///<absolute-path>; URLs with a non-empty
// authority (unix://host/path) are rejected because the path component would
// be ambiguous. On Windows, unix:///C:%5Cpath%5Cthv.sock round-trips back to
// C:\path\thv.sock by stripping the synthetic leading slash that net/url
// inserts in front of the drive letter.
func ParseUnixSocketPath(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid unix socket URL: %w", err)
	}
	if u.Scheme != "unix" {
		return "", fmt.Errorf("unix socket URL must start with unix://: %s", rawURL)
	}
	if u.Host != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("unix socket path must be absolute (use unix:///<path>): %s", rawURL)
	}
	path := u.Path
	if path == "" {
		return "", fmt.Errorf("empty unix socket path")
	}

	// On Windows AF_UNIX paths, the listener emits unix:///C:%5Cpath%5C..., which
	// url.Parse returns as Path="/C:\path\..." with a synthetic leading slash.
	// Strip it before any further validation so filepath.IsAbs sees the actual
	// drive-letter form.
	if len(path) >= 3 && path[0] == '/' && isASCIILetter(path[1]) && path[2] == ':' {
		path = path[1:]
	}

	// Check for traversal before Clean resolves it away.
	if strings.Contains(path, "..") {
		return "", fmt.Errorf("unix socket path must not contain '..': %s", path)
	}

	path = filepath.Clean(path)

	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("unix socket path must be absolute: %s", path)
	}

	return path, nil
}

// ParseNamedPipeURL extracts and validates the pipe name from an npipe:// URL
// and returns the full Windows pipe path (e.g. \\.\pipe\thv-api). The URL
// shape is strict: npipe://<name> with no path, query, fragment, userinfo, or
// port. The name itself must match a conservative charset, fit within the
// 247-char limit imposed by CreateNamedPipeW after the prefix, and must not
// be one of the legacy reserved Windows device names (CON, NUL, COM1, etc.).
// The returned path always uses the lowercase form of the name because the
// pipe namespace is case-insensitive at the kernel layer.
func ParseNamedPipeURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid named pipe URL: %w", err)
	}
	if u.Scheme != "npipe" {
		return "", fmt.Errorf("named pipe URL must start with npipe://: %s", rawURL)
	}
	if u.Path != "" || u.Opaque != "" || u.RawQuery != "" ||
		u.Fragment != "" || u.User != nil || u.Port() != "" {
		return "", fmt.Errorf("named pipe URL must be of the form npipe://<name>: %s", rawURL)
	}
	name := strings.ToLower(u.Hostname())
	if name == "" {
		return "", fmt.Errorf("empty named pipe name")
	}
	if len(name) > maxNamedPipeNameLen {
		return "", fmt.Errorf("named pipe name exceeds %d characters: %d", maxNamedPipeNameLen, len(name))
	}
	if !namedPipeNamePattern.MatchString(name) {
		return "", fmt.Errorf("named pipe name has invalid characters (allowed: A-Z, a-z, 0-9, ., _, -): %s", name)
	}
	if _, reserved := reservedNamedPipeNames[name]; reserved {
		return "", fmt.Errorf("named pipe name is a reserved Windows device name: %s", name)
	}
	return NamedPipePrefix + name, nil
}

// isASCIILetter reports whether b is an ASCII letter [A-Za-z]. Used by
// ParseUnixSocketPath to detect Windows drive-letter prefixes after url.Parse.
func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}
