// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/stacklok/toolhive/pkg/authserver"
	spiffeauth "github.com/stacklok/toolhive/pkg/authserver/spiffe"
)

const (
	certificateCheckInterval  = 30 * time.Second
	listenerReadHeaderTimeout = 10 * time.Second
	listenerReadTimeout       = 30 * time.Second
	listenerWriteTimeout      = 30 * time.Second
	listenerIdleTimeout       = 60 * time.Second
)

type tlsKeyPairCache struct {
	mu            sync.Mutex
	certFile      string
	keyFile       string
	pair          tls.Certificate
	leaf          *x509.Certificate
	loadedHash    string
	failedHash    string
	lastCheck     time.Time
	checkInterval time.Duration
}

type caHintCache struct {
	mu   sync.Mutex
	hash string
	pool *x509.CertPool
}

type tlsListener struct {
	host              string
	port              int
	server            *http.Server
	listener          net.Listener
	keyPair           *tlsKeyPairCache
	caHints           caHintCache
	authorities       func() []*x509.Certificate
	requestClientCert bool
	mu                sync.Mutex
}

func newTLSListener(host string, cfg *authserver.TLSListenerRunConfig, routes map[string]http.Handler, authorities func() []*x509.Certificate, requestClientCert bool) (*tlsListener, error) {
	cache, err := newTLSKeyPairCache(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, err
	}
	return newTLSListenerWithCache(host, cache, routes, authorities, requestClientCert), nil
}

func newTLSListenerWithCache(host string, cache *tlsKeyPairCache, routes map[string]http.Handler, authorities func() []*x509.Certificate, requestClientCert bool) *tlsListener {
	mux := http.NewServeMux()
	for pattern, handler := range routes {
		mux.Handle(pattern, handler)
	}
	base := &tlsListener{host: host, port: TLSListenerPort, keyPair: cache, authorities: authorities, requestClientCert: requestClientCert}
	clientAuth := tls.NoClientCert
	if requestClientCert {
		clientAuth = tls.RequestClientCert
	}
	tlsConfig := &tls.Config{
		MinVersion:       tls.VersionTLS12,
		ClientAuth:       clientAuth,
		GetCertificate:   cache.getCertificate,
		VerifyConnection: logClientCertificate,
	}
	if requestClientCert {
		tlsConfig.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
			clone := tlsConfig.Clone()
			clone.ClientCAs = base.caPool()
			if clone.ClientCAs == nil {
				slog.Debug("auth server TLS listener requesting a client certificate without a CA hint")
			} else {
				slog.Debug("auth server TLS listener requesting a client certificate",
					"acceptable_ca_count", len(clone.ClientCAs.Subjects())) //nolint:staticcheck // Subjects is only used for the hint count.
			}
			return clone, nil
		}
	}
	base.server = &http.Server{
		Handler:           spiffeauth.Middleware(mux),
		ReadHeaderTimeout: listenerReadHeaderTimeout,
		ReadTimeout:       listenerReadTimeout,
		WriteTimeout:      listenerWriteTimeout,
		IdleTimeout:       listenerIdleTimeout,
		ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
		TLSConfig:         tlsConfig,
	}
	return base
}

func (l *tlsListener) start() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listener != nil {
		return errors.New("auth server TLS listener already started")
	}
	address := net.JoinHostPort(l.host, fmt.Sprintf("%d", l.port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("bind auth server TLS listener on %s: %w", address, err)
	}
	l.listener = tls.NewListener(listener, l.server.TLSConfig)
	l.server.Addr = address
	slog.Debug("auth server TLS listener started",
		"address", address, "request_client_certificate", l.requestClientCert)
	go func() {
		if err := l.server.Serve(l.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("auth server TLS listener stopped unexpectedly", "error", err)
		}
	}()
	return nil
}

func (l *tlsListener) shutdown(ctx context.Context) error {
	l.mu.Lock()
	server, listener := l.server, l.listener
	l.mu.Unlock()
	err := server.Shutdown(ctx)
	// Shutdown only closes listeners Serve has already registered; close ours
	// directly so the port is freed even if the serve goroutine has not run yet.
	if listener != nil {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = errors.Join(err, closeErr)
		}
	}
	return err
}

// logClientCertificate records, at debug level, which client certificate (if
// any) a connection presented. It never rejects a connection: verification
// happens in the OAuth client-authentication strategy. The certificate itself
// is not logged, only its identity fields.
func logClientCertificate(state tls.ConnectionState) error {
	if len(state.PeerCertificates) == 0 {
		slog.Debug("auth server TLS listener connection without a client certificate")
		return nil
	}
	leaf := state.PeerCertificates[0]
	uris := make([]string, 0, len(leaf.URIs))
	for _, uri := range leaf.URIs {
		uris = append(uris, uri.String())
	}
	slog.Debug("auth server TLS listener connection with a client certificate",
		"uri_sans", uris,
		"issuer", leaf.Issuer.String(),
		"chain_length", len(state.PeerCertificates),
		"not_after", leaf.NotAfter)
	return nil
}

func (l *tlsListener) caPool() *x509.CertPool {
	if l.authorities == nil {
		return nil
	}
	authorities := l.authorities()
	hash := sha256.New()
	for _, cert := range authorities {
		if cert != nil {
			_, _ = hash.Write(cert.Raw)
		}
	}
	hashValue := hex.EncodeToString(hash.Sum(nil))
	l.caHints.mu.Lock()
	defer l.caHints.mu.Unlock()
	if hashValue == l.caHints.hash {
		return l.caHints.pool
	}
	if len(authorities) == 0 {
		l.caHints.hash, l.caHints.pool = hashValue, nil
		return nil
	}
	pool := x509.NewCertPool()
	for _, cert := range authorities {
		if cert != nil {
			pool.AddCert(cert)
		}
	}
	l.caHints.hash, l.caHints.pool = hashValue, pool
	return pool
}

func newTLSKeyPairCache(certFile, keyFile string) (*tlsKeyPairCache, error) {
	cache := &tlsKeyPairCache{certFile: certFile, keyFile: keyFile, checkInterval: certificateCheckInterval}
	if err := cache.load(); err != nil {
		return nil, err
	}
	return cache, nil
}

func (c *tlsKeyPairCache) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.lastCheck) >= c.checkInterval {
		c.lastCheck = time.Now()
		certBytes, certErr := os.ReadFile(c.certFile)
		keyBytes, keyErr := os.ReadFile(c.keyFile)
		if certErr != nil || keyErr != nil {
			slog.Warn("failed to reload auth server TLS listener certificate", "error", errors.Join(certErr, keyErr))
		} else {
			hash := pairHash(certBytes, keyBytes)
			if hash != c.loadedHash && hash != c.failedHash {
				pair, err := tls.X509KeyPair(certBytes, keyBytes)
				if err != nil {
					c.failedHash = hash
					slog.Warn("failed to reload auth server TLS listener certificate", "error", err)
				} else if err := c.setPair(pair, hash); err != nil {
					c.failedHash = hash
					slog.Warn("failed to parse auth server TLS listener certificate", "error", err)
				}
			}
		}
	}
	return &c.pair, nil
}

func (c *tlsKeyPairCache) load() error {
	certBytes, err := os.ReadFile(c.certFile)
	if err != nil {
		return fmt.Errorf("read certificate file: %w", err)
	}
	keyBytes, err := os.ReadFile(c.keyFile)
	if err != nil {
		return fmt.Errorf("read key file: %w", err)
	}
	pair, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return err
	}
	return c.setPair(pair, pairHash(certBytes, keyBytes))
}

func (c *tlsKeyPairCache) setPair(pair tls.Certificate, hash string) error {
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	c.pair, c.leaf, c.loadedHash = pair, leaf, hash
	c.failedHash = ""
	return nil
}

func pairHash(certBytes, keyBytes []byte) string {
	h := sha256.New()
	_, _ = h.Write(certBytes)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(keyBytes)
	return hex.EncodeToString(h.Sum(nil))
}
