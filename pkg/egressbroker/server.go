// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	envoycore "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoytls "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	envoyauth "github.com/envoyproxy/go-control-plane/envoy/service/auth/v3"
	envoydiscovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	envoysecret "github.com/envoyproxy/go-control-plane/envoy/service/secret/v3"
	"google.golang.org/genproto/googleapis/rpc/code"
	"google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"
)

// AuthorizationServer is the Envoy ext_authz gRPC endpoint. It translates
// Check requests into CredentialInjector decisions. Every failure mode —
// malformed request, missing destination, injector deny, internal error —
// returns a denial: ext_authz must never pass traffic on doubt.
type AuthorizationServer struct {
	envoyauth.UnimplementedAuthorizationServer
	injector *CredentialInjector
}

// NewAuthorizationServer builds the ext_authz endpoint on an injector.
func NewAuthorizationServer(injector *CredentialInjector) (*AuthorizationServer, error) {
	if injector == nil {
		return nil, fmt.Errorf("egressbroker: injector must not be nil")
	}
	return &AuthorizationServer{injector: injector}, nil
}

// Check implements envoy.service.auth.v3.Authorization.
func (s *AuthorizationServer) Check(ctx context.Context, req *envoyauth.CheckRequest) (*envoyauth.CheckResponse, error) {
	dest, err := destinationFromCheckRequest(req)
	if err != nil {
		slog.DebugContext(ctx, "egressbroker: denying request with unparseable destination", "error", err)
		return deniedResponse(codes.InvalidArgument, "unparseable destination"), nil
	}
	decision := s.injector.Evaluate(ctx, dest)
	if !decision.Allow {
		slog.DebugContext(ctx, "egressbroker: denied egress",
			"host", dest.Host, "method", dest.Method, "reason", decision.DenyReason)
		return deniedResponse(codes.PermissionDenied, decision.DenyReason), nil
	}
	return okResponse(decision.HeaderName, decision.HeaderValue), nil
}

// destinationFromCheckRequest extracts host/method/path from the attribute
// context. Host is port-stripped and lowercased; an empty host or method
// fails closed.
func destinationFromCheckRequest(req *envoyauth.CheckRequest) (Destination, error) {
	httpReq := req.GetAttributes().GetRequest().GetHttp()
	if httpReq == nil {
		return Destination{}, fmt.Errorf("check request carries no HTTP attributes")
	}
	host := httpReq.GetHeaders()[":authority"]
	if host == "" {
		host = httpReq.GetHost()
	}
	host, _, err := net.SplitHostPort(host)
	if err != nil {
		// SplitHostPort fails when there is no port — use the raw value.
		host = httpReq.GetHeaders()[":authority"]
		if host == "" {
			host = httpReq.GetHost()
		}
	}
	host = normalizeHost(host)
	if host == "" {
		return Destination{}, fmt.Errorf("check request carries no destination host")
	}
	method := httpReq.GetMethod()
	if method == "" {
		return Destination{}, fmt.Errorf("check request carries no HTTP method")
	}
	path := httpReq.GetPath()
	if path == "" {
		path = "/"
	}
	// CONNECT requests carry no scheme; others do — the path for CONNECT is
	// authority-form, which the policy's path prefixes will correctly refuse
	// unless "/" is allowed. Envoy routes CONNECT through upgrade config, and
	// the tunneled requests are re-checked individually.
	return Destination{Host: host, Method: method, Path: path}, nil
}

// okResponse allows the request and mutates exactly one header. The header
// value is the only credential material that ever crosses this boundary.
func okResponse(headerName, headerValue string) *envoyauth.CheckResponse {
	return &envoyauth.CheckResponse{
		Status: &status.Status{Code: int32(code.Code_OK)},
		HttpResponse: &envoyauth.CheckResponse_OkResponse{
			OkResponse: &envoyauth.OkHttpResponse{
				Headers: []*envoycore.HeaderValueOption{{
					Header: &envoycore.HeaderValue{Key: headerName, Value: headerValue},
				}},
				// Never echo the credential anywhere but the upstream request
				// header: response headers and dynamic metadata stay empty.
			},
		},
	}
}

// deniedResponse denies with a static body carrying the reason (never
// credential material). Envoy maps a non-OK status to 403 by default; the
// denied response makes that explicit.
func deniedResponse(rpcCode codes.Code, reason string) *envoyauth.CheckResponse {
	return &envoyauth.CheckResponse{
		Status: &status.Status{Code: int32(rpcCode)}, //nolint:gosec // G115: gRPC codes are small non-negative ints
		HttpResponse: &envoyauth.CheckResponse_DeniedResponse{
			DeniedResponse: &envoyauth.DeniedHttpResponse{
				Body: "egress denied: " + reason,
			},
		},
	}
}

// SecretDiscoveryServer mints per-SNI TLS-bump certificates on demand (D9)
// and serves them to Envoy over SDS. Only the TLS listener's per-host
// certificate resources are served; requests for any other resource (or for
// the CA key) are refused. The CA private key is never served.
//
// Certs are minted once per hostname and cached for the process lifetime
// (short-lived leaves; a process restart re-mints).
type SecretDiscoveryServer struct {
	envoysecret.UnimplementedSecretDiscoveryServiceServer
	ca     *BumpCA
	policy *EgressPolicy

	mu    sync.Mutex
	cache map[string]*envoytls.Secret
}

// NewSecretDiscoveryServer builds the SDS endpoint. policy constrains which
// hostnames certs are minted for — a cert is only ever minted for a host the
// egress policy allowlists (fail closed: no policy match, no cert).
func NewSecretDiscoveryServer(ca *BumpCA, policy *EgressPolicy) (*SecretDiscoveryServer, error) {
	if ca == nil {
		return nil, fmt.Errorf("egressbroker: bump CA must not be nil")
	}
	if policy == nil {
		return nil, fmt.Errorf("egressbroker: policy must not be nil")
	}
	return &SecretDiscoveryServer{ca: ca, policy: policy, cache: map[string]*envoytls.Secret{}}, nil
}

// FetchSecrets implements SecretDiscoveryService. Used by Envoy's
// on-demand SDS for downstream TLS contexts.
func (s *SecretDiscoveryServer) FetchSecrets(
	ctx context.Context, req *envoydiscovery.DiscoveryRequest,
) (*envoydiscovery.DiscoveryResponse, error) {
	if len(req.GetResourceNames()) == 0 {
		return nil, grpcstatus.Error(codes.InvalidArgument, "SDS request must name resources")
	}
	resources := make([]*anypb.Any, 0, len(req.GetResourceNames()))
	for _, name := range req.GetResourceNames() {
		secret, err := s.secretForHost(name)
		if err != nil {
			// Fail closed: no cert for a non-allowlisted host. The listener
			// handshake fails instead of falling back to any other cert.
			slog.DebugContext(ctx, "egressbroker: refusing to mint cert", "resource", name, "error", err)
			return nil, grpcstatus.Error(codes.PermissionDenied, "no certificate for resource")
		}
		packed, err := anypb.New(secret)
		if err != nil {
			return nil, grpcstatus.Error(codes.Internal, "failed to pack secret")
		}
		resources = append(resources, packed)
	}
	return &envoydiscovery.DiscoveryResponse{
		VersionInfo: fmt.Sprintf("%d", time.Now().UnixNano()),
		Resources:   resources,
		TypeUrl:     "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret",
	}, nil
}

// secretForHost returns (minting+caching) the bump cert for a resource name
// of the form "host:<hostname>". The hostname must be policy-allowlisted.
func (s *SecretDiscoveryServer) secretForHost(resourceName string) (*envoytls.Secret, error) {
	host, found := strings.CutPrefix(resourceName, "host:")
	if !found || host == "" {
		return nil, fmt.Errorf("unknown SDS resource name")
	}
	host = normalizeHost(host)
	if _, ok := s.policy.ProviderFor(host); !ok {
		return nil, fmt.Errorf("host not in egress policy")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if cached, ok := s.cache[host]; ok {
		return cached, nil
	}
	certPEM, keyPEM, err := s.ca.MintLeaf(host, time.Now())
	if err != nil {
		return nil, err
	}
	secret := &envoytls.Secret{
		Name: resourceName,
		Type: &envoytls.Secret_TlsCertificate{
			TlsCertificate: &envoytls.TlsCertificate{
				CertificateChain: &envoycore.DataSource{
					Specifier: &envoycore.DataSource_InlineBytes{InlineBytes: certPEM},
				},
				PrivateKey: &envoycore.DataSource{
					Specifier: &envoycore.DataSource_InlineBytes{InlineBytes: keyPEM},
				},
			},
		},
	}
	s.cache[host] = secret
	return secret, nil
}

// Server bundles the gRPC listener serving ext_authz + SDS on one socket
// (loopback-only).
type Server struct {
	grpcServer *grpc.Server
	listener   net.Listener
}

// NewServer creates the gRPC server on listenAddress:port, registering the
// ext_authz and SDS services.
func NewServer(
	listenAddress string,
	port int,
	authz *AuthorizationServer,
	sds *SecretDiscoveryServer,
) (*Server, error) {
	if authz == nil || sds == nil {
		return nil, fmt.Errorf("egressbroker: authz and sds servers must not be nil")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(listenAddress, fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to listen on %s:%d: %w", listenAddress, port, err)
	}
	grpcServer := grpc.NewServer()
	envoyauth.RegisterAuthorizationServer(grpcServer, authz)
	envoysecret.RegisterSecretDiscoveryServiceServer(grpcServer, sds)
	return &Server{grpcServer: grpcServer, listener: listener}, nil
}

// Run serves until ctx is canceled, then gracefully stops.
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()
	if err := s.grpcServer.Serve(s.listener); err != nil {
		return fmt.Errorf("egressbroker: gRPC server failed: %w", err)
	}
	return nil
}
