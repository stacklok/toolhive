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
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
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
	audit    AuditLogger // nil: deny events from unparseable requests are not audited
	metrics  *BrokerMetrics
	identity PodIdentity
	podName  string
}

// NewAuthorizationServer builds the ext_authz endpoint on an injector.
func NewAuthorizationServer(injector *CredentialInjector) (*AuthorizationServer, error) {
	if injector == nil {
		return nil, fmt.Errorf("egressbroker: injector must not be nil")
	}
	return &AuthorizationServer{injector: injector}, nil
}

// WithObservability attaches the audit/metrics sinks used for Check-level
// denials that never reach the injector (unparseable destination).
func (s *AuthorizationServer) WithObservability(
	audit AuditLogger, metrics *BrokerMetrics, identity PodIdentity, podName string,
) *AuthorizationServer {
	s.audit = audit
	s.metrics = metrics
	s.identity = identity
	s.podName = podName
	return s
}

// Check implements envoy.service.auth.v3.Authorization.
func (s *AuthorizationServer) Check(ctx context.Context, req *envoyauth.CheckRequest) (*envoyauth.CheckResponse, error) {
	dest, err := destinationFromCheckRequest(req)
	if err != nil {
		slog.DebugContext(ctx, "egressbroker: denying request with unparseable destination", "error", err)
		if s.audit != nil {
			s.audit.Deny(ctx, DenyEvent{
				InjectEvent: AuditEvent(s.identity, s.podName, Destination{}, ""),
				Reason:      DenyReasonMalformed,
			})
		}
		s.metrics.RecordDenial(ctx, s.identity.MCPServer, "", DenyReasonMalformed)
		return deniedResponse(codes.InvalidArgument, "unparseable destination"), nil
	}
	requestID := requestIDFromCheckRequest(req)
	decision := s.injector.Evaluate(ctx, dest, requestID)
	if !decision.Allow {
		slog.DebugContext(ctx, "egressbroker: denied egress",
			"host", dest.Host, "method", dest.Method, "reason", decision.Reason)
		return deniedResponse(codes.PermissionDenied, decision.DenyDetail), nil
	}
	return okResponse(decision.HeaderName, decision.HeaderValue), nil
}

// requestIDFromCheckRequest returns the Envoy x-request-id header (present:
// the rendered bootstrap enables the request-id extension, so Envoy generates
// one when the downstream did not supply it). Empty when the header map is
// absent; the injector skips scan-correlation for an empty id.
func requestIDFromCheckRequest(req *envoyauth.CheckRequest) string {
	return req.GetAttributes().GetRequest().GetHttp().GetHeaders()[requestIDHeader]
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
					// The injected credential REPLACES anything the workload
					// sent — explicit so a go-control-plane default change can
					// never silently turn this into an append.
					AppendAction: envoycore.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
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
// ext_authz, SDS, and ext_proc (response scanner, D6c) services on one
// loopback socket.
func NewServer(
	listenAddress string,
	port int,
	authz *AuthorizationServer,
	sds *SecretDiscoveryServer,
	extproc *ExternalProcessorServer,
) (*Server, error) {
	if authz == nil || sds == nil || extproc == nil {
		return nil, fmt.Errorf("egressbroker: authz, sds, and ext_proc servers must not be nil")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(listenAddress, fmt.Sprintf("%d", port)))
	if err != nil {
		return nil, fmt.Errorf("egressbroker: failed to listen on %s:%d: %w", listenAddress, port, err)
	}
	grpcServer := grpc.NewServer()
	envoyauth.RegisterAuthorizationServer(grpcServer, authz)
	envoysecret.RegisterSecretDiscoveryServiceServer(grpcServer, sds)
	extprocv3.RegisterExternalProcessorServer(grpcServer, extproc)
	return &Server{grpcServer: grpcServer, listener: listener}, nil
}

// gracefulStopTimeout bounds the drain on shutdown: a hung ext_proc stream
// must not wedge the pod past its termination grace period. After the budget
// the server is stopped forcefully (in-flight RPCs error out; Envoy's
// failure_mode_allow governs those responses).
const gracefulStopTimeout = 5 * time.Second

// NewTestServer wraps an arbitrary grpc.Server + listener in a Server so the
// shutdown path (GracefulStop + bounded Stop) is testable without the
// production service trio. Test-only constructor.
func NewTestServer(grpcServer *grpc.Server, listener net.Listener) *Server {
	return &Server{grpcServer: grpcServer, listener: listener}
}

// GracefulStopTimeoutForTest exposes the drain budget so the wedge test can
// bound its own wait (kept out of the constant's scope so tests cannot
// mutate it).
func GracefulStopTimeoutForTest() time.Duration { return gracefulStopTimeout }

// Run serves until ctx is canceled, then stops with a bounded drain. On
// cancellation GracefulStop closes the listener and drains in-flight RPCs;
// a hung ext_proc stream would block that drain forever, so after
// gracefulStopTimeout the server is force-stopped — wedged streams error
// out and Envoy's failure_mode_allow governs those responses.
//
// Run does NOT wait for Serve to return on the forced path: grpc-go's Serve
// blocks until the graceful stop fully completes (it waits out hung
// handlers), so Run itself bounds the wait at the drain budget and returns
// while the wedge unwinds in the background.
func (s *Server) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()
	go func() {
		<-ctx.Done()
		timer := time.NewTimer(gracefulStopTimeout)
		defer timer.Stop()
		<-timer.C
		slog.Warn("egressbroker: graceful gRPC stop exceeded the drain budget; forcing stop",
			"timeout", gracefulStopTimeout)
		s.grpcServer.Stop()
	}()
	serveDone := make(chan error, 1)
	go func() { serveDone <- s.grpcServer.Serve(s.listener) }()
	select {
	case err := <-serveDone:
		if err != nil {
			return fmt.Errorf("egressbroker: gRPC server failed: %w", err)
		}
		return nil
	case <-ctx.Done():
		// Shutdown in progress: give the graceful drain its budget, then
		// return — the forced Stop above unwinds any wedge asynchronously.
		timer := time.NewTimer(gracefulStopTimeout)
		defer timer.Stop()
		select {
		case err := <-serveDone:
			if err != nil {
				return fmt.Errorf("egressbroker: gRPC server failed: %w", err)
			}
			return nil
		case <-timer.C:
			return nil
		}
	}
}
