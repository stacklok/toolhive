package networking

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"golang.org/x/oauth2"
)

var privateIPBlocks []*net.IPNet

// HttpTimeout is the timeout for outgoing HTTP requests
const HttpTimeout = 30 * time.Second

const httpsScheme = "https"

// Dialer control function for validating addresses prior to connection
func protectedDialerControl(_, address string, _ syscall.RawConn) error {
	err := AddressReferencesPrivateIp(address)
	if err != nil {
		return err
	}

	return nil
}

// ValidatingTransport is for validating URLs prior to request
type ValidatingTransport struct {
	Transport http.RoundTripper
}

// RoundTrip validates the request URL prior to forwarding
func (t *ValidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Skip validation if INSECURE_DISABLE_URL_VALIDATION is set
	if strings.EqualFold(os.Getenv("INSECURE_DISABLE_URL_VALIDATION"), "true") {
		return t.Transport.RoundTrip(req)
	}

	// Check for valid URL specification
	parsedUrl, err := url.Parse(req.URL.String())
	if err != nil {
		return nil, fmt.Errorf("the supplied URL %s is malformed", req.URL.String())
	}

	// Check for HTTPS scheme
	if parsedUrl.Scheme != httpsScheme {
		return nil, fmt.Errorf("the supplied URL %s is not HTTPS scheme", req.URL.String())
	}

	return t.Transport.RoundTrip(req)
}

// createTokenSourceFromFile creates an oauth2.TokenSource from a token file
func createTokenSourceFromFile(tokenFile string) (oauth2.TokenSource, error) {
	tokenBytes, err := os.ReadFile(tokenFile) // #nosec G304 - tokenFile path is provided by user via CLI flag
	if err != nil {
		return nil, fmt.Errorf("failed to read auth token file: %w", err)
	}

	// Remove any trailing newlines/whitespace
	tokenStr := strings.TrimSpace(string(tokenBytes))
	if tokenStr == "" {
		return nil, fmt.Errorf("auth token file is empty")
	}

	// Create a static token source
	token := &oauth2.Token{
		AccessToken: tokenStr,
		TokenType:   "Bearer",
	}

	return oauth2.StaticTokenSource(token), nil
}

// HttpClientBuilder provides a fluent interface for building HTTP clients
type HttpClientBuilder struct {
	clientTimeout         time.Duration
	tlsHandshakeTimeout   time.Duration
	responseHeaderTimeout time.Duration
	caCertPath            string
	authTokenFile         string
	allowPrivate          bool
}

// NewHttpClientBuilder returns a new HttpClientBuilder
func NewHttpClientBuilder() *HttpClientBuilder {
	return &HttpClientBuilder{
		clientTimeout:         HttpTimeout,
		tlsHandshakeTimeout:   10 * time.Second,
		responseHeaderTimeout: 10 * time.Second,
	}
}

// WithCABundle sets the CA certificate bundle path
func (b *HttpClientBuilder) WithCABundle(path string) *HttpClientBuilder {
	b.caCertPath = path
	return b
}

// WithTokenFromFile sets the auth token file path
func (b *HttpClientBuilder) WithTokenFromFile(path string) *HttpClientBuilder {
	b.authTokenFile = path
	return b
}

// WithPrivateIPs allows connections to private IP addresses
func (b *HttpClientBuilder) WithPrivateIPs(allow bool) *HttpClientBuilder {
	b.allowPrivate = allow
	return b
}

// Build creates the configured HTTP client
func (b *HttpClientBuilder) Build() (*http.Client, error) {
	transport := &http.Transport{
		TLSHandshakeTimeout:   b.tlsHandshakeTimeout,
		ResponseHeaderTimeout: b.responseHeaderTimeout,
	}

	if !b.allowPrivate {
		transport.DialContext = (&net.Dialer{
			Control: protectedDialerControl,
		}).DialContext
	}

	if b.caCertPath != "" {
		caCert, err := os.ReadFile(b.caCertPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA certificate bundle: %w", err)
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate bundle")
		}

		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		}
		transport.TLSClientConfig.RootCAs = caCertPool
	}

	// Start with validation transport
	var clientTransport http.RoundTripper = &ValidatingTransport{
		Transport: transport,
	}

	// Add auth transport if token file is provided using oauth2.Transport
	if b.authTokenFile != "" {
		tokenSource, err := createTokenSourceFromFile(b.authTokenFile)
		if err != nil {
			return nil, fmt.Errorf("failed to create token source: %w", err)
		}

		// oauth2.Transport wraps our existing transport and adds Bearer token authentication
		clientTransport = &oauth2.Transport{
			Source: tokenSource,
			Base:   clientTransport, // Preserves our ValidatingTransport
		}
	}

	client := &http.Client{
		Transport: clientTransport,
		Timeout:   HttpTimeout,
	}

	return client, nil
}
