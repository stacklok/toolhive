// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package externalauthsupport defines which MCPExternalAuthConfig types each
// consumer kind accepts, shared by the operator controllers.
package externalauthsupport

import (
	"fmt"
	"slices"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/pkg/vmcp/auth/converters"
)

// Consumer identifies a resource kind that consumes an MCPExternalAuthConfig.
type Consumer string

const (
	// ConsumerMCPServer identifies MCPServer.spec.externalAuthConfigRef.
	ConsumerMCPServer Consumer = "MCPServer"
	// ConsumerMCPRemoteProxy identifies MCPRemoteProxy.spec.externalAuthConfigRef.
	ConsumerMCPRemoteProxy Consumer = "MCPRemoteProxy"
	// ConsumerVirtualMCPServer identifies VirtualMCPServer outgoing authentication.
	ConsumerVirtualMCPServer Consumer = "VirtualMCPServer"
	// ConsumerMCPServerEntry identifies MCPServerEntry.spec.externalAuthConfigRef.
	ConsumerMCPServerEntry Consumer = "MCPServerEntry"
)

// vmcpAppliedTypes are the types a backend resource (MCPServer, MCPRemoteProxy)
// accepts without applying them itself. In discovered mode a VirtualMCPServer
// reads the reference from the backend resource and applies the strategy when
// it calls that backend, so the reference has to live on the backend and the
// backend has to reconcile normally. Rejecting these types there would take the
// backend, and with it discovered mode, down.
var vmcpAppliedTypes = []mcpv1beta1.ExternalAuthType{
	mcpv1beta1.ExternalAuthTypeHeaderInjection,
	mcpv1beta1.ExternalAuthTypeUpstreamInject,
	mcpv1beta1.ExternalAuthTypeXAA,
}

// supportMatrix records which auth types each consumer accepts. It is
// treated as immutable after initialization.
//
// The vMCP-backed rows (VirtualMCPServer, MCPServerEntry) are derived from the
// converter registry, so registering a new converter extends their support
// automatically.
//
// The MCPServer and MCPRemoteProxy rows hold the types the proxy runner
// applies, plus vmcpAppliedTypes. They are enforced at the run-config dispatch
// in controllerutil.AddExternalAuthConfigOptions, which returns
// UnsupportedTypeError for anything outside the row — the tests in
// controllerutil exercise that dispatch against every type, so these rows
// cannot silently drift from what the dispatcher implements.
//
// OBO appears for every consumer. MCPServer and MCPRemoteProxy additionally
// require incoming OIDC authentication for it, which is checked separately
// (controllerutil.ValidateOBOIncomingAuth).
var supportMatrix = map[Consumer]map[mcpv1beta1.ExternalAuthType]struct{}{
	ConsumerMCPServer: backendRow(
		mcpv1beta1.ExternalAuthTypeTokenExchange,
		mcpv1beta1.ExternalAuthTypeUnauthenticated,
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
		mcpv1beta1.ExternalAuthTypeOBO,
	),
	ConsumerMCPRemoteProxy: backendRow(
		mcpv1beta1.ExternalAuthTypeTokenExchange,
		mcpv1beta1.ExternalAuthTypeBearerToken,
		mcpv1beta1.ExternalAuthTypeUnauthenticated,
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer,
		mcpv1beta1.ExternalAuthTypeAWSSts,
		mcpv1beta1.ExternalAuthTypeOBO,
	),
	ConsumerVirtualMCPServer: converterBackedTypes(),
	ConsumerMCPServerEntry:   converterBackedTypes(),
}

// backendRow builds the support row of a backend resource: the types its proxy
// runner applies, plus the ones a VirtualMCPServer applies on its behalf.
func backendRow(applied ...mcpv1beta1.ExternalAuthType) map[mcpv1beta1.ExternalAuthType]struct{} {
	row := make(map[mcpv1beta1.ExternalAuthType]struct{}, len(applied)+len(vmcpAppliedTypes))
	for _, authType := range applied {
		row[authType] = struct{}{}
	}
	for _, authType := range vmcpAppliedTypes {
		row[authType] = struct{}{}
	}
	return row
}

// converterBackedTypes builds a support row from the types registered in the
// vMCP converter registry — the code path these consumers actually route
// through at conversion time.
func converterBackedTypes() map[mcpv1beta1.ExternalAuthType]struct{} {
	registered := converters.DefaultRegistry().RegisteredTypes()
	row := make(map[mcpv1beta1.ExternalAuthType]struct{}, len(registered))
	for _, authType := range registered {
		row[authType] = struct{}{}
	}
	return row
}

// UnsupportedTypeError reports an MCPExternalAuthConfig type that cannot be
// used by a particular consumer.
type UnsupportedTypeError struct {
	Consumer Consumer
	AuthType mcpv1beta1.ExternalAuthType
}

// Error implements error.
func (e *UnsupportedTypeError) Error() string {
	return fmt.Sprintf("external auth type %q is not supported by %s", e.AuthType, e.Consumer)
}

// Supports reports whether consumer accepts a reference to authType.
func Supports(consumer Consumer, authType mcpv1beta1.ExternalAuthType) bool {
	supported, knownConsumer := supportMatrix[consumer]
	if !knownConsumer {
		return false
	}
	_, ok := supported[authType]
	return ok
}

// AppliedByVirtualMCPServer reports whether consumer accepts authType without
// applying it: the type takes effect only when a VirtualMCPServer discovers the
// reference on that backend.
func AppliedByVirtualMCPServer(consumer Consumer, authType mcpv1beta1.ExternalAuthType) bool {
	if consumer != ConsumerMCPServer && consumer != ConsumerMCPRemoteProxy {
		return false
	}
	return slices.Contains(vmcpAppliedTypes, authType)
}

// Validate returns an UnsupportedTypeError when consumer does not accept
// authType.
func Validate(consumer Consumer, authType mcpv1beta1.ExternalAuthType) error {
	if Supports(consumer, authType) {
		return nil
	}

	return &UnsupportedTypeError{
		Consumer: consumer,
		AuthType: authType,
	}
}
