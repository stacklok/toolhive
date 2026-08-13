// SPDX-FileCopyrightText: Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package externalauthsupport defines the external-auth compatibility matrix
// shared by operator controllers.
package externalauthsupport

import (
	"fmt"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
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

// supportMatrix is the single support contract shared by all consumers. It is
// private and treated as immutable after initialization. Keep it in sync with
// the field documentation and the exhaustive matrix test in support_test.go.
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
	ConsumerVirtualMCPServer: {
		mcpv1beta1.ExternalAuthTypeTokenExchange:   {},
		mcpv1beta1.ExternalAuthTypeHeaderInjection: {},
		mcpv1beta1.ExternalAuthTypeUnauthenticated: {},
		mcpv1beta1.ExternalAuthTypeAWSSts:          {},
		mcpv1beta1.ExternalAuthTypeUpstreamInject:  {},
		mcpv1beta1.ExternalAuthTypeOBO:             {},
		mcpv1beta1.ExternalAuthTypeXAA:             {},
	},
	ConsumerMCPServerEntry: {
		mcpv1beta1.ExternalAuthTypeTokenExchange:   {},
		mcpv1beta1.ExternalAuthTypeHeaderInjection: {},
		mcpv1beta1.ExternalAuthTypeUnauthenticated: {},
		mcpv1beta1.ExternalAuthTypeAWSSts:          {},
		mcpv1beta1.ExternalAuthTypeUpstreamInject:  {},
		mcpv1beta1.ExternalAuthTypeOBO:             {},
		mcpv1beta1.ExternalAuthTypeXAA:             {},
	},
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

// Supports reports whether consumer implements authType. OBO is included for
// every consumer because enterprise builds may register the required handler;
// upstream-only builds reject OBO separately with EnterpriseRequired.
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
