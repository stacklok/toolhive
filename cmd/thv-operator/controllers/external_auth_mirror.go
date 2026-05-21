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

// Status Condition Parity machinery for #5347. The probe reads a referenced
// MCPExternalAuthConfig's Valid condition; the per-consumer mirror functions
// transcribe its reason+message onto the consumer CR's parallel condition.
// Without this, an obo-typed MCPExternalAuthConfig in an upstream-only build
// surfaces EnterpriseRequired only on the referenced resource and the consumer
// status reports only the generic dispatch failure.

// fallbackInvalidReason is substituted when a source surfaces Valid=False with
// an empty Reason. metav1.Condition requires Reason to be non-empty and the
// apiserver rejects empty-Reason patches, so the mirror cannot copy an empty
// Reason verbatim without trapping the consumer in a noisy reconcile loop.
const fallbackInvalidReason = "InvalidExternalAuthConfig"

// mirroredInvalidExternalAuthConfigError signals that a referenced
// MCPExternalAuthConfig had Status.Conditions[Valid]=False. Carries the
// source's reason+message so callers can surface them on the consumer's
// status without re-fetching the object, and satisfies the error interface
// so it can travel through error-returning APIs (notably
// convertBackendAuthConfigToVMCP -> buildOutgoingAuthConfig).
type mirroredInvalidExternalAuthConfigError struct {
	Reason  string
	Message string
}

func (e *mirroredInvalidExternalAuthConfigError) Error() string {
	return fmt.Sprintf("invalid (%s): %s", e.Reason, e.Message)
}

// mirroredExternalAuthConfigInvalid returns a non-nil typed error when the
// source's Valid condition is False, or nil otherwise. Use
// [mirroredReasonFromError] to recover the reason from a wrapped chain.
func mirroredExternalAuthConfigInvalid(
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) *mirroredInvalidExternalAuthConfigError {
	validCond := meta.FindStatusCondition(externalAuthConfig.Status.Conditions, mcpv1beta1.ConditionTypeValid)
	if validCond == nil || validCond.Status != metav1.ConditionFalse {
		return nil
	}
	reason := validCond.Reason
	if reason == "" {
		reason = fallbackInvalidReason
	}
	return &mirroredInvalidExternalAuthConfigError{Reason: reason, Message: validCond.Message}
}

// mirroredReasonFromError returns the mirrored source reason embedded in err
// (via *mirroredInvalidExternalAuthConfigError) or "" if err does not carry
// one. Walks the wrap chain via errors.As so it remains correct when callers
// wrap the typed error with fmt.Errorf("...: %w", err) before passing it on
// (notably buildOutgoingAuthConfig in the VirtualMCPServer pipeline).
func mirroredReasonFromError(err error) string {
	var mirrored *mirroredInvalidExternalAuthConfigError
	if stderrors.As(err, &mirrored) {
		return mirrored.Reason
	}
	return ""
}

// mirrorInvalidOnMCPServer mirrors the source's Valid=False condition onto the
// MCPServer's ExternalAuthConfigValidated condition. When the source is healed
// (Valid=True or absent), it clears any stale mirror so the condition does not
// outlive its cause. Returns (true, err) when a False mirror was written so
// the caller can mark Phase=Failed; (false, nil) otherwise.
//
// See package-level Status Condition Parity comment for the #5347 motivation.
func mirrorInvalidOnMCPServer(
	m *mcpv1beta1.MCPServer,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (bool, error) {
	mirrored := mirroredExternalAuthConfigInvalid(externalAuthConfig)
	if mirrored == nil {
		meta.RemoveStatusCondition(&m.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
		return false, nil
	}
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeExternalAuthConfigValidated,
		Status:             metav1.ConditionFalse,
		Reason:             mirrored.Reason,
		Message:            mirrored.Message,
		ObservedGeneration: m.Generation,
	})
	return true, fmt.Errorf("MCPExternalAuthConfig %s/%s: %w", m.Namespace, externalAuthConfig.Name, mirrored)
}

// mirrorInvalidOnRemoteProxy mirrors the source's Valid=False condition onto
// the MCPRemoteProxy's ExternalAuthConfigValidated condition. When the source
// is healed, it clears any stale mirror defensively — the downstream True
// writer in handleExternalAuthConfig also sets the success reason, but a
// future early return between this site and that writer would otherwise leak
// a stale False. Returns (true, err) when a False mirror was written so the
// caller can short-circuit; (false, nil) otherwise.
func mirrorInvalidOnRemoteProxy(
	proxy *mcpv1beta1.MCPRemoteProxy,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (bool, error) {
	mirrored := mirroredExternalAuthConfigInvalid(externalAuthConfig)
	if mirrored == nil {
		meta.RemoveStatusCondition(
			&proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated)
		return false, nil
	}
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
		Status:             metav1.ConditionFalse,
		Reason:             mirrored.Reason,
		Message:            mirrored.Message,
		ObservedGeneration: proxy.Generation,
	})
	return true, fmt.Errorf("MCPExternalAuthConfig %s/%s: %w", proxy.Namespace, externalAuthConfig.Name, mirrored)
}
