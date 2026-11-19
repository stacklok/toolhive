// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command spiffe-client obtains SPIFFE workload credentials for operator E2E tests.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"

	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

const requestTimeout = 30 * time.Second

// jwtSVIDSignatureAlgorithms are the algorithms go-spiffe accepts for JWT-SVIDs.
var jwtSVIDSignatureAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.PS256, jose.PS384, jose.PS512,
}

type identityOutput struct {
	SPIFFEID       string `json:"spiffe_id"`
	X509LeafSerial string `json:"x509_leaf_serial"`
	JWTExpiry      string `json:"jwt_expiry"`
	JWTKeyID       string `json:"jwt_key_id"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "spiffe-client:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("command is required")
	}

	switch args[0] {
	case "hold":
		return hold(args[1:])
	case "identity":
		return identity(args[1:], stdout)
	case "token":
		return token(args[1:], stdout)
	case "forged-token":
		return forgedToken(args[1:], stdout)
	default:
		return errors.New("unknown command")
	}
}

func hold(args []string) error {
	if len(args) != 0 {
		return errors.New("hold does not accept arguments")
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	<-signals
	return nil
}

func identity(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("identity", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socket := flags.String("socket", "", "SPIFFE Workload API Unix socket URL")
	jwtAudience := flags.String("jwt-audience", "", "JWT-SVID audience")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid identity arguments")
	}
	if flags.NArg() != 0 || !validSocket(*socket) || strings.TrimSpace(*jwtAudience) == "" {
		return errors.New("identity requires a Unix socket URL and JWT audience")
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	x509SVID, err := workloadapi.FetchX509SVID(ctx, workloadapi.WithAddr(*socket))
	if err != nil {
		return fmt.Errorf("fetch X.509-SVID: %w", err)
	}
	if x509SVID == nil || len(x509SVID.Certificates) == 0 || x509SVID.Certificates[0] == nil {
		return errors.New("workload API returned an invalid X.509-SVID")
	}
	jwtSVID, err := workloadapi.FetchJWTSVID(ctx, jwtsvid.Params{Audience: *jwtAudience}, workloadapi.WithAddr(*socket))
	if err != nil {
		return fmt.Errorf("fetch JWT-SVID: %w", err)
	}
	if jwtSVID == nil {
		return errors.New("workload API returned an invalid JWT-SVID")
	}
	keyID, err := jwtKeyID(jwtSVID.Marshal())
	if err != nil {
		return err
	}

	return json.NewEncoder(stdout).Encode(identityOutput{
		SPIFFEID:       x509SVID.ID.String(),
		X509LeafSerial: x509SVID.Certificates[0].SerialNumber.String(),
		JWTExpiry:      jwtSVID.Expiry.UTC().Format(time.RFC3339),
		JWTKeyID:       keyID,
	})
}

// jwtKeyID returns the kid header of a JWT-SVID the Workload API issued.
func jwtKeyID(token string) (string, error) {
	parsed, err := jwt.ParseSigned(token, jwtSVIDSignatureAlgorithms)
	if err != nil {
		return "", fmt.Errorf("parse JWT-SVID header: %w", err)
	}
	if len(parsed.Headers) != 1 || parsed.Headers[0].KeyID == "" {
		return "", errors.New("JWT-SVID header has no key ID")
	}
	return parsed.Headers[0].KeyID, nil
}

func token(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("token", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	method := flags.String("method", "", "SPIFFE credential method: x509 or jwt")
	socket := flags.String("socket", "", "SPIFFE Workload API Unix socket URL")
	issuer := flags.String("issuer", "", "authorization server HTTPS issuer")
	clientID := flags.String("client-id", "", "OAuth client ID")
	resource := flags.String("resource", "", "OAuth resource indicator")
	scope := flags.String("scope", "", "OAuth scope")
	serverCAFile := flags.String("server-ca-file", "", "authorization server public CA PEM file")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid token arguments")
	}
	if flags.NArg() != 0 || !validSocket(*socket) || !validIssuer(*issuer) ||
		anyBlank(*clientID, *resource, *scope, *serverCAFile) {
		return errors.New("token requires all flags with valid socket and HTTPS issuer URLs")
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	form := clientCredentialsForm(*clientID, *resource, *scope)
	client, err := tokenHTTPClient(*serverCAFile)
	if err != nil {
		return err
	}

	if *method != "x509" && *method != "jwt" {
		return errors.New("token method must be x509 or jwt")
	}
	switch *method {
	case "x509":
		if err := configureX509Client(ctx, client, *socket); err != nil {
			return err
		}
	case "jwt":
		assertion, err := fetchJWTAssertion(ctx, *socket, *issuer)
		if err != nil {
			return err
		}
		form.Set("client_assertion_type", spiffeauth.SPIFFEJWTAssertionType)
		form.Set("client_assertion", assertion)
	}

	return requestAndPrintToken(ctx, client, *issuer, *method, form, stdout)
}

// forgedToken requests a token with a SPIFFE credential that no trust bundle
// issued: a self-signed X.509 certificate or a JWT signed by a throwaway key,
// both carrying the given SPIFFE ID. It never contacts the Workload API.
func forgedToken(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("forged-token", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	method := flags.String("method", "", "SPIFFE credential method: x509 or jwt")
	rawSPIFFEID := flags.String("spiffe-id", "", "SPIFFE ID the forged credential claims")
	issuer := flags.String("issuer", "", "authorization server HTTPS issuer")
	clientID := flags.String("client-id", "", "OAuth client ID")
	resource := flags.String("resource", "", "OAuth resource indicator")
	scope := flags.String("scope", "", "OAuth scope")
	serverCAFile := flags.String("server-ca-file", "", "authorization server public CA PEM file")
	keyID := flags.String("key-id", "", "JWS kid for the jwt method; a random one when empty")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid forged-token arguments")
	}
	if flags.NArg() != 0 || !validIssuer(*issuer) || anyBlank(*clientID, *resource, *scope, *serverCAFile) {
		return errors.New("forged-token requires all flags with a valid HTTPS issuer URL")
	}
	if *method != "x509" && *method != "jwt" {
		return errors.New("forged-token method must be x509 or jwt")
	}
	id, err := spiffeid.FromString(*rawSPIFFEID)
	if err != nil {
		return fmt.Errorf("invalid SPIFFE ID: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate forged key: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	form := clientCredentialsForm(*clientID, *resource, *scope)
	client, err := tokenHTTPClient(*serverCAFile)
	if err != nil {
		return err
	}
	switch *method {
	case "x509":
		certificate, err := selfSignedX509SVID(id, key)
		if err != nil {
			return err
		}
		// GetClientCertificate rather than Certificates: the Go client withholds
		// a certificate whose issuer is not among the server's advertised client
		// CAs, and this one never is.
		client.Transport.(*http.Transport).TLSClientConfig.GetClientCertificate =
			func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return certificate, nil }
	case "jwt":
		assertion, err := forgedJWTSVID(id, *issuer, *keyID, key)
		if err != nil {
			return err
		}
		form.Set("client_assertion_type", spiffeauth.SPIFFEJWTAssertionType)
		form.Set("client_assertion", assertion)
	}
	return requestAndPrintToken(ctx, client, *issuer, *method, form, stdout)
}

// selfSignedX509SVID returns a self-signed leaf that satisfies the server's
// X.509-SVID leaf profile, so chain verification against the trust bundle is
// the only check it fails.
func selfSignedX509SVID(id spiffeid.ID, key *ecdsa.PrivateKey) (*tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate forged certificate serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(5 * time.Minute),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{id.URL()},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, fmt.Errorf("create forged certificate: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, fmt.Errorf("parse forged certificate: %w", err)
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

// forgedJWTSVID signs a JWT-SVID-shaped assertion for id with a key no trust
// bundle carries, within the server's assertion lifetime cap. A keyID naming a
// bundle key makes the server reach signature verification rather than fail
// the key lookup.
func forgedJWTSVID(id spiffeid.ID, issuer, keyID string, key *ecdsa.PrivateKey) (string, error) {
	if keyID == "" {
		keyID = "forged-" + rand.Text()
	}
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: keyID}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return "", fmt.Errorf("create forged JWT signer: %w", err)
	}
	now := time.Now()
	return jwt.Signed(signer).Claims(jwt.Claims{
		Subject:  id.String(),
		Audience: jwt.Audience{issuer},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Serialize()
}

func clientCredentialsForm(clientID, resource, scope string) url.Values {
	return url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {clientID},
		"resource":   {resource},
		"scope":      {scope},
	}
}

func requestAndPrintToken(
	ctx context.Context, client *http.Client, issuer, method string, form url.Values, stdout io.Writer,
) error {
	endpoint, err := discoverTokenEndpoint(ctx, client, issuer, "spiffe_"+method)
	if err != nil {
		return err
	}
	accessToken, err := requestToken(ctx, client, endpoint, form)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, accessToken)
	return err
}

func configureX509Client(ctx context.Context, client *http.Client, socket string) error {
	x509SVID, err := workloadapi.FetchX509SVID(ctx, workloadapi.WithAddr(socket))
	if err != nil {
		return fmt.Errorf("fetch X.509-SVID: %w", err)
	}
	if x509SVID == nil || len(x509SVID.Certificates) == 0 || x509SVID.Certificates[0] == nil || x509SVID.PrivateKey == nil {
		return errors.New("workload API returned an invalid X.509-SVID")
	}
	client.Transport.(*http.Transport).TLSClientConfig.Certificates = []tls.Certificate{{
		Certificate: certificatesDER(x509SVID.Certificates),
		PrivateKey:  x509SVID.PrivateKey,
		Leaf:        x509SVID.Certificates[0],
	}}
	return nil
}

func fetchJWTAssertion(ctx context.Context, socket, issuer string) (string, error) {
	jwtSVID, err := workloadapi.FetchJWTSVID(ctx, jwtsvid.Params{Audience: issuer}, workloadapi.WithAddr(socket))
	if err != nil {
		return "", fmt.Errorf("fetch JWT-SVID: %w", err)
	}
	if jwtSVID == nil || jwtSVID.Marshal() == "" {
		return "", errors.New("workload API returned an invalid JWT-SVID")
	}
	return jwtSVID.Marshal(), nil
}

func certificatesDER(certificates []*x509.Certificate) [][]byte {
	der := make([][]byte, len(certificates))
	for i, certificate := range certificates {
		der[i] = certificate.Raw
	}
	return der
}

func tokenHTTPClient(serverCAFile string) (*http.Client, error) {
	pemData, err := os.ReadFile(serverCAFile) // #nosec G304 -- CA bundle path is explicitly supplied for this E2E client.
	if err != nil {
		return nil, fmt.Errorf("read authorization server CA file: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pemData) {
		return nil, errors.New("authorization server CA file contains no certificates")
	}

	return &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		},
	}, nil
}

// discoverTokenEndpoint reads the RFC 8414 metadata and fails unless the server
// advertises the client_credentials grant and authMethod, so a server that
// serves the grant but omits it from discovery is caught here.
func discoverTokenEndpoint(ctx context.Context, client *http.Client, issuer, authMethod string) (string, error) {
	metadataURL := strings.TrimRight(issuer, "/") + "/.well-known/oauth-authorization-server"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return "", fmt.Errorf("create metadata request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("fetch authorization server metadata: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata endpoint returned HTTP %d", response.StatusCode)
	}
	var metadata struct {
		TokenEndpoint                     string   `json:"token_endpoint"`
		GrantTypesSupported               []string `json:"grant_types_supported"`
		TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&metadata); err != nil {
		return "", errors.New("decode authorization server metadata")
	}
	if metadata.TokenEndpoint == "" {
		return "", errors.New("metadata did not include a token endpoint")
	}
	if !slices.Contains(metadata.GrantTypesSupported, "client_credentials") {
		return "", errors.New("metadata does not advertise the client_credentials grant")
	}
	if !slices.Contains(metadata.TokenEndpointAuthMethodsSupported, authMethod) {
		return "", fmt.Errorf("metadata does not advertise the %s authentication method", authMethod)
	}
	return metadata.TokenEndpoint, nil
}

func requestToken(ctx context.Context, client *http.Client, endpoint string, form url.Values) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("create token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("submit token request: %w", err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", fmt.Errorf("token endpoint returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", errors.New("decode token response")
	}
	if payload.AccessToken == "" {
		return "", errors.New("token response did not include an access token")
	}
	return payload.AccessToken, nil
}

func anyBlank(values ...string) bool {
	return slices.ContainsFunc(values, func(v string) bool { return strings.TrimSpace(v) == "" })
}

func validSocket(socket string) bool {
	parsed, err := url.Parse(socket)
	return err == nil && parsed.Scheme == "unix" && parsed.Path != ""
}

func validIssuer(issuer string) bool {
	parsed, err := url.Parse(issuer)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.RawQuery == "" && parsed.Fragment == ""
}
