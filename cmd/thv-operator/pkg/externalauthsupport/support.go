// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package externalauthsupport defines which MCPExternalAuthConfig types each
// consumer kind implements, shared by the operator controllers.
package externalauthsupport

import (
	"fmt"

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

// supportMatrix records which auth types each consumer implements. It is
// treated as immutable after initialization.
//
// The vMCP-backed rows (VirtualMCPServer, MCPServerEntry) are derived from the
// converter registry, so registering a new converter extends their support
// automatically. The MCPServer and MCPRemoteProxy rows are declared here and
// enforced at the run-config dispatch in controllerutil.AddExternalAuthConfigOptions,
// which returns UnsupportedTypeError for anything outside the row — the tests
// in controllerutil exercise that dispatch against every type, so these rows
// cannot silently drift from what the dispatcher implements.
//
// OBO appears for every consumer because enterprise builds may register the
// required handler; upstream-only builds reject OBO separately with
// EnterpriseRequired.
var supportMatrix = map[Consumer]map[mcpv1beta1.ExternalAuthType]struct{}{
	ConsumerMCPServer: {
		mcpv1beta1.ExternalAuthTypeTokenExchange:      {},
		mcpv1beta1.ExternalAuthTypeUnauthenticated:    {},
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer: {},
		mcpv1beta1.ExternalAuthTypeOBO:                {},
	},
	ConsumerMCPRemoteProxy: {
		mcpv1beta1.ExternalAuthTypeTokenExchange:      {},
		mcpv1beta1.ExternalAuthTypeBearerToken:        {},
		mcpv1beta1.ExternalAuthTypeUnauthenticated:    {},
		mcpv1beta1.ExternalAuthTypeEmbeddedAuthServer: {},
		mcpv1beta1.ExternalAuthTypeAWSSts:             {},
		mcpv1beta1.ExternalAuthTypeOBO:                {},
	},
	ConsumerVirtualMCPServer: converterBackedTypes(),
	ConsumerMCPServerEntry:   converterBackedTypes(),
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

// Supports reports whether consumer implements authType.
func Supports(consumer Consumer, authType mcpv1beta1.ExternalAuthType) bool {
	supported, knownConsumer := supportMatrix[consumer]
	if !knownConsumer {
		return false
	}
	_, ok := supported[authType]
	return ok
}

// Validate returns an UnsupportedTypeError when consumer does not implement
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
