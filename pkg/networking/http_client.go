package networking

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

var privateIPBlocks []*net.IPNet

// HttpTimeout is the timeout for outgoing HTTP requests
const HttpTimeout = 30 * time.Second

func init() {
	for _, cidr := range []string{
		"127.0.0.0/8",    // IPv4 loopback
		"10.0.0.0/8",     // RFC1918
		"172.16.0.0/12",  // RFC1918
		"192.168.0.0/16", // RFC1918
		"169.254.0.0/16", // RFC3927 link-local
		"::1/128",        // IPv6 loopback
		"fe80::/10",      // IPv6 link-local
		"fc00::/7",       // IPv6 unique local addr
	} {
		_, block, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Errorf("parse error on %q: %v", cidr, err))
		}
		privateIPBlocks = append(privateIPBlocks, block)
	}
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	for _, block := range privateIPBlocks {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// Dialer control function for validating addresses prior to connection
func protectedDialerControl(network, address string, _ syscall.RawConn) error {

	fmt.Printf("protectedDialerControl: %s, %s\n", network, address)

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	// Check for a private IP address or loopback
	ip := net.ParseIP(host)
	if isPrivateIP(ip) {
		return errors.New("private IP address not allowed")
	}
	return nil
}

// ValidatingTransport is for validating URLs prior to request
type ValidatingTransport struct {
	Transport http.RoundTripper
}

// RoundTrip validates the request URL prior to forwarding
func (t *ValidatingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Check for valid URL specification
	parsedUrl, err := url.Parse(req.URL.String())
	if err != nil {
		fmt.Print(err)
		return nil, fmt.Errorf("the supplied URL %s is malformed", req.URL.String())
	}

	// Check for HTTPS scheme
	if parsedUrl.Scheme != "https" {
		return nil, fmt.Errorf("the supplied URL %s is not HTTPS scheme", req.URL.String())
	}

	return t.Transport.RoundTrip(req)
}

// GetProtectedHttpClient returns a new http client with a protected dialer and URL validation
func GetProtectedHttpClient() *http.Client {

	protectedTransport := &http.Transport{
		DialContext: (&net.Dialer{
			Control: protectedDialerControl,
		}).DialContext,
	}

	client := &http.Client{
		Transport: &ValidatingTransport{
			Transport: protectedTransport,
		},
		Timeout: HttpTimeout,
	}

	return client
}
