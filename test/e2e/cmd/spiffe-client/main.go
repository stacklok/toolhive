// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Command spiffe-client obtains SPIFFE workload credentials for operator E2E tests.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spiffe/go-spiffe/v2/svid/jwtsvid"
	"github.com/spiffe/go-spiffe/v2/workloadapi"
)

const (
	requestTimeout = 30 * time.Second
	// Must match pkg/authserver/spiffe.SPIFFEJWTAssertionType exactly — this
	// binary can't import that package (it needs to build as a standalone
	// ko image), so the string is duplicated here. Transposed segments here
	// silently produce a generic "Unknown client_assertion_type" 400 from
	// the token endpoint, confirmed against a real cluster.
	spiffeJWTAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-spiffe"
)

type identityOutput struct {
	SPIFFEID       string `json:"spiffe_id"`
	X509LeafSerial string `json:"x509_leaf_serial"`
	JWTExpiry      string `json:"jwt_expiry"`
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

	return json.NewEncoder(stdout).Encode(identityOutput{
		SPIFFEID:       x509SVID.ID.String(),
		X509LeafSerial: x509SVID.Certificates[0].SerialNumber.String(),
		JWTExpiry:      jwtSVID.Expiry.UTC().Format(time.RFC3339),
	})
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
		strings.TrimSpace(*clientID) == "" || strings.TrimSpace(*resource) == "" ||
		strings.TrimSpace(*scope) == "" || strings.TrimSpace(*serverCAFile) == "" {
		return errors.New("token requires all flags with valid socket and HTTPS issuer URLs")
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	form := url.Values{
		"grant_type": {"client_credentials"},
		"client_id":  {*clientID},
		"resource":   {*resource},
		"scope":      {*scope},
	}
	client, err := tokenHTTPClient(*serverCAFile)
	if err != nil {
		return err
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
		form.Set("client_assertion_type", spiffeJWTAssertionType)
		form.Set("client_assertion", assertion)
	default:
		return errors.New("token method must be x509 or jwt")
	}

	accessToken, err := requestToken(ctx, client, *issuer, form)
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

func requestToken(ctx context.Context, client *http.Client, issuer string, form url.Values) (string, error) {
	endpoint := strings.TrimRight(issuer, "/") + "/oauth/token"
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

func validSocket(socket string) bool {
	parsed, err := url.Parse(socket)
	return err == nil && parsed.Scheme == "unix" && parsed.Path != ""
}

func validIssuer(issuer string) bool {
	parsed, err := url.Parse(issuer)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.RawQuery == "" && parsed.Fragment == ""
}
