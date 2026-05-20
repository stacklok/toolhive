// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	stderrors "errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// mirroredInvalidExternalAuthConfigError signals that a referenced
// MCPExternalAuthConfig had Status.Conditions[Valid]=False and the source's
// reason+message should be mirrored onto the consumer's status. Callers may
// extract the reason via mirroredReasonFromError when they need to propagate
// it through layers that only carry a generic error (notably the
// VirtualMCPServer AuthConfigError aggregator).
type mirroredInvalidExternalAuthConfigError struct {
	Reason  string
	Message string
}

func (e *mirroredInvalidExternalAuthConfigError) Error() string {
	return fmt.Sprintf("referenced MCPExternalAuthConfig is invalid (%s): %s", e.Reason, e.Message)
}

// mirroredExternalAuthConfigInvalid inspects a fetched MCPExternalAuthConfig
// and returns a non-nil *mirroredInvalidExternalAuthConfigError when its Valid
// condition is False, or nil when Valid is True/absent. The returned value
// implements error and carries the source's reason and message so callers can
// surface them on the consumer's status without re-fetching the object.
//
// Returning the concrete pointer (rather than an (error, string) tuple) keeps
// callers from needing errors.As to recover the reason/message fields when
// they have the value in hand. The error path still satisfies the standard
// error interface, so callers that propagate it through layers that only
// carry error (notably convertBackendAuthConfigToVMCP -> buildOutgoingAuthConfig)
// can recover the same pointer via mirroredReasonFromError below.
//
// Consumer reconcilers (MCPServer, MCPRemoteProxy, VirtualMCPServer) use this
// probe to mirror the source's Valid=False condition onto the consumer CR,
// closing the Status Condition Parity gap described in #5347. Without the
// mirror, an obo-typed MCPExternalAuthConfig in an upstream-only build would
// surface EnterpriseRequired only on the referenced resource, leaving the
// consumer's status to report only the generic dispatch failure.
func mirroredExternalAuthConfigInvalid(
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) *mirroredInvalidExternalAuthConfigError {
	validCond := meta.FindStatusCondition(externalAuthConfig.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	if validCond == nil || validCond.Status != metav1.ConditionFalse {
		return nil
	}
	return &mirroredInvalidExternalAuthConfigError{
		Reason:  validCond.Reason,
		Message: validCond.Message,
	}
}

// mirroredReasonFromError returns the mirrored source reason embedded in err
// (via *mirroredInvalidExternalAuthConfigError) or "" if err does not carry
// one. Used by the VirtualMCPServer buildOutgoingAuthConfig pipeline to
// propagate the source's reason onto the per-backend AuthConfigError without
// re-fetching the MCPExternalAuthConfig.
func mirroredReasonFromError(err error) string {
	var mirrored *mirroredInvalidExternalAuthConfigError
	if stderrors.As(err, &mirrored) {
		return mirrored.Reason
	}
	return ""
}
