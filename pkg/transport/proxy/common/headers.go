package common

import (
	"net"
	"net/http"
)

// ForwardedHeaders contains extracted X-Forwarded-* header values.
type ForwardedHeaders struct {
	Proto  string // X-Forwarded-Proto
	Host   string // X-Forwarded-Host (may include port)
	Port   string // X-Forwarded-Port
	Prefix string // X-Forwarded-Prefix
}

// ExtractForwardedHeaders extracts X-Forwarded-* headers from the request.
// Only extracts headers if trustProxyHeaders is true.
// Always extracts X-Forwarded-Proto regardless of trustProxyHeaders setting.
func ExtractForwardedHeaders(r *http.Request, trustProxyHeaders bool) ForwardedHeaders {
	headers := ForwardedHeaders{}

	// Always extract X-Forwarded-Proto (used for scheme detection)
	headers.Proto = r.Header.Get("X-Forwarded-Proto")

	if !trustProxyHeaders {
		return headers
	}

	// Extract other headers only if trust is enabled
	headers.Host = r.Header.Get("X-Forwarded-Host")
	headers.Port = r.Header.Get("X-Forwarded-Port")
	headers.Prefix = r.Header.Get("X-Forwarded-Prefix")

	return headers
}

// JoinHostPort combines host and port, handling existing ports in host.
// If host already contains a port, it's stripped before adding the new port.
func JoinHostPort(host, port string) string {
	if port == "" {
		return host
	}

	// Strip any existing port from host
	if hostOnly, _, err := net.SplitHostPort(host); err == nil {
		host = hostOnly
	}

	return net.JoinHostPort(host, port)
}
