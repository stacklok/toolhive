// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package egressbroker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	typev3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
)

// ExternalProcessorServer is the Envoy ext_proc gRPC endpoint implementing
// response-side credential scanning (ADR-0001 D6c). The ext_authz Check that
// injects a credential records (x-request-id → scan record); this service
// looks the request-id up on each response and scans the headers + buffered
// body for the injected credential value (exact and base64-encoded).
//
// Correlation model: Envoy forwards x-request-id in the ext_proc dynamic
// metadata (namespace "io.toolhive.egress", key "request_id") because the
// response-path ext_proc cannot see request headers. The rendered bootstrap
// sets that metadata with a Lua HTTP filter running BEFORE ext_authz (route
// filter_metadata values are stored literally — no formatter substitution —
// so they cannot carry it).
//
// Failure posture: the DOCUMENTED fail-open default lives in the RENDERED
// ENVOY CONFIG (failure_mode_allow: a dead/unreachable/slow scanner passes
// responses); this service additionally applies the configured posture
// (failOpen) to in-band decisions — an unknown request-id (deny path,
// scanner restart, TTL eviction, direct hit) and an over-cap body pass with
// a metric when failOpen, and suppress the response (502) when !failOpen.
// Detection of an actual leak ALWAYS blocks — the failure mode governs
// scanner unavailability only, never matches.
type ExternalProcessorServer struct {
	extprocv3.UnimplementedExternalProcessorServer
	tokens   *TokenMap
	bounds   ScannerBounds
	failOpen bool
	identity PodIdentity
	metrics  *BrokerMetrics
	audit    AuditLogger
}

// NewExternalProcessorServer wires the scanner. Fails loudly on nil/invalid
// dependencies (constructor validation: a misconfigured scanner must not
// start).
func NewExternalProcessorServer(
	tokens *TokenMap,
	bounds ScannerBounds,
	failOpen bool,
	identity PodIdentity,
	metrics *BrokerMetrics,
	audit AuditLogger,
) (*ExternalProcessorServer, error) {
	if tokens == nil {
		return nil, fmt.Errorf("egressbroker: token map must not be nil")
	}
	if err := validateScannerBounds(bounds); err != nil {
		return nil, err
	}
	if audit == nil {
		return nil, fmt.Errorf("egressbroker: audit logger must not be nil")
	}
	if identity.MCPServer == "" {
		return nil, fmt.Errorf("egressbroker: pod identity is incomplete; refusing to build response scanner")
	}
	return &ExternalProcessorServer{
		tokens:   tokens,
		bounds:   bounds,
		failOpen: failOpen,
		identity: identity,
		metrics:  metrics,
		audit:    audit,
	}, nil
}

// Process implements envoy.service.ext_proc.v3.ExternalProcessor. One stream
// corresponds to one HTTP request/response exchange; messages arrive in
// request-then-response order (request phase is a pass-through — injection
// stays in ext_authz).
func (s *ExternalProcessorServer) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	state := &StreamState{}
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			// Transport-level failure: nothing to decide; the filter's own
			// failure_mode_allow governs the in-flight response.
			return err
		}
		resp := s.Handle(stream.Context(), state, req)
		if resp == nil {
			continue
		}
		if err := stream.Send(resp); err != nil {
			return err
		}
	}
}

// StreamState carries the correlation across the phases of one stream.
type StreamState struct {
	requestID string
	record    ScanRecord
	known     bool
	host      string
}

// Handle computes the response (nil = no response needed) for one processing
// request, carrying per-stream correlation state. It never returns an error
// to the stream: every internal failure is decided locally per the
// fail-open/fail-closed posture so a scanner bug can never wedge every
// upstream call of the workload. Exported so tests can drive the scanner
// without a gRPC stream; production callers go through Process.
func (s *ExternalProcessorServer) Handle(ctx context.Context, st *StreamState, req *extprocv3.ProcessingRequest,
) *extprocv3.ProcessingResponse {
	switch r := req.GetRequest().(type) {
	case *extprocv3.ProcessingRequest_ResponseHeaders:
		// The request-id arrives via the Lua-set dynamic metadata (the
		// response-side ext_proc cannot see request headers; the rendered
		// bootstrap copies x-request-id into io.toolhive.egress upstream of
		// ext_authz).
		st.requestID = requestIDFromMetadata(req.GetMetadataContext())
		st.host = requestHostFromMetadata(req.GetMetadataContext())
		record, ok := s.tokens.Lookup(st.requestID)
		if !ok {
			// Unknown request-id: deny path (no injection), scanner restart,
			// TTL eviction, or a direct hit. Fail-open: pass + metric;
			// fail-closed: suppress (502) — nothing proves this response does
			// not echo a credential.
			s.metrics.RecordScan(ctx, s.identity.MCPServer, "", metricResultUnknown, "")
			if !s.failOpen {
				slog.WarnContext(ctx, "egressbroker: unknown request-id; suppressing response (fail-closed)",
					"mcpserver", s.identity.MCPServer, "host", st.host)
				return s.suppress()
			}
			return &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseHeaders{},
			}
		}
		st.record = record
		st.known = true
		if scanHeaders(r.ResponseHeaders.GetHeaders(), record.Needles) {
			return s.block(ctx, st, LeakLocationHeader)
		}
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseHeaders{},
		}

	case *extprocv3.ProcessingRequest_ResponseBody:
		if !st.known {
			// Unknown request-id was already counted at the header phase.
			return &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseBody{},
			}
		}
		result := scanBody(r.ResponseBody.GetBody(), st.record.Needles, s.bounds.MaxBodyBytes)
		switch result {
		case scanLeak:
			return s.block(ctx, st, LeakLocationBody)
		case scanOversize:
			// Body not scanned (the cap is a cost bound, not a security
			// boundary — the allowlist + destination binding are). Record ONLY
			// the skip: a result=ok datapoint here would double-count the body
			// phase as scanned when it was not.
			s.metrics.RecordScanSkipped(ctx, s.identity.MCPServer, st.record.Provider)
			slog.WarnContext(ctx, "egressbroker: response body over scan cap; body not scanned",
				"mcpserver", s.identity.MCPServer, "provider", st.record.Provider,
				"cap_bytes", s.bounds.MaxBodyBytes)
			if !s.failOpen {
				return s.suppress()
			}
			return &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseBody{},
			}
		case scanClean:
			// fall through to the pass-through below
		}
		s.metrics.RecordScan(ctx, s.identity.MCPServer, st.record.Provider, metricResultOK, "")
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_ResponseBody{},
		}

	default:
		// Request bodies/trailers and response trailers are not configured for
		// delivery; answer with the matching pass-through variant if one ever
		// arrives so the filter never stalls.
		return passThroughFor(req)
	}
}

// block suppresses a leaking response: 502 + generic body, audit + metric.
// The matched value NEVER appears in any log, metric, or response.
func (s *ExternalProcessorServer) block(
	ctx context.Context, st *StreamState, where LeakLocation,
) *extprocv3.ProcessingResponse {
	s.metrics.RecordScan(ctx, s.identity.MCPServer, st.record.Provider, metricResultLeak, string(where))
	s.audit.Leak(ctx, LeakEvent{
		MCPServer: s.identity.MCPServer,
		Provider:  st.record.Provider,
		Host:      st.host,
		RequestID: st.requestID,
		Where:     where,
	})
	slog.WarnContext(ctx, "egressbroker: response suppressed (injected credential echoed back)",
		"mcpserver", s.identity.MCPServer, "provider", st.record.Provider, "where", string(where))
	return s.suppress()
}

// suppress is the 502 + generic-body immediate response (no credential
// material, no reason detail that would help an attacker tune the evasion).
func (*ExternalProcessorServer) suppress() *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &extprocv3.ImmediateResponse{
				Status:  &typev3.HttpStatus{Code: typev3.StatusCode_BadGateway},
				Body:    []byte("response suppressed by egress policy"),
				Details: "egress_broker_response_leak",
			},
		},
	}
}

// passThroughFor returns the empty response variant matching the incoming
// request variant so unconfigured phases never stall the filter.
func passThroughFor(req *extprocv3.ProcessingRequest) *extprocv3.ProcessingResponse {
	switch req.GetRequest().(type) {
	case *extprocv3.ProcessingRequest_RequestBody:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestBody{}}
	case *extprocv3.ProcessingRequest_RequestTrailers:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_RequestTrailers{}}
	case *extprocv3.ProcessingRequest_ResponseTrailers:
		return &extprocv3.ProcessingResponse{Response: &extprocv3.ProcessingResponse_ResponseTrailers{}}
	default:
		return nil
	}
}
