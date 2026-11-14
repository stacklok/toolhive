// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"fmt"
	"net/url"
	urlpath "path"
	"path/filepath"
	"strings"
)

// UnixSocketURL builds a unix:// URL for a filesystem socket path.
//
// POSIX absolute paths already start with / and must keep the three-slash
// form unix:///path. Windows drive-letter paths get a synthetic leading slash
// so net/url round-trips (unix:///C:%5C... instead of unix://C:\..., which
// url.Parse rejects as an invalid port).
func UnixSocketURL(address string) string {
	return (&url.URL{Scheme: "unix", Path: unixSocketURLPath(address)}).String()
}

func unixSocketURLPath(address string) string {
	if isDriveLetterPath(address) {
		return "/" + address
	}
	return address
}

// parseUnixSocketPath converts the Path from a parsed unix:// URL into a
// filesystem socket path. Drive-letter forms stay native (C:\...) on every
// GOOS so a discovery file written on Windows is parseable in tests. POSIX
// forms stay slash-separated, including the older four-slash alias
// unix:////tmp/foo.sock (url.Parse Path is //tmp/foo.sock).
func parseUnixSocketPath(rawPath string) (string, error) {
	if rawPath == "" {
		return "", fmt.Errorf("empty unix socket path")
	}
	if isDriveLetterURLPath(rawPath) {
		return parseDriveLetterSocketPath(rawPath)
	}
	return parsePOSIXSocketPath(rawPath)
}

func isDriveLetterPath(p string) bool {
	return len(p) >= 2 && isASCIILetter(p[0]) && p[1] == ':'
}

// Windows current-directory-relative form (C:foo) is not absolute.
func isAbsoluteDriveLetterPath(p string) bool {
	return isDriveLetterPath(p) && len(p) >= 3 && (p[2] == '\\' || p[2] == '/')
}

func isDriveLetterURLPath(p string) bool {
	if isDriveLetterPath(p) {
		return true
	}
	return len(p) >= 3 && p[0] == '/' && isDriveLetterPath(p[1:])
}

func parseDriveLetterSocketPath(p string) (string, error) {
	if p[0] == '/' {
		p = p[1:]
	}
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("unix socket path must not contain '..': %s", p)
	}
	cleaned := filepath.Clean(p)
	if !isAbsoluteDriveLetterPath(cleaned) {
		return "", fmt.Errorf("unix socket path must be absolute: %s", p)
	}
	return cleaned, nil
}

func parsePOSIXSocketPath(p string) (string, error) {
	if strings.Contains(p, "..") {
		return "", fmt.Errorf("unix socket path must not contain '..': %s", p)
	}
	cleaned := urlpath.Clean(p)
	if !strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf("unix socket path must be absolute: %s", p)
	}
	return cleaned, nil
}
