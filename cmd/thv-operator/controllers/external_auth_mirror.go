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

// ownedByEmbeddedAuthServerConfigValidation reports whether conditions'
// entry for conditionType (if any) was set by
// handleInvalidEmbeddedAuthServerConfig rather than by the mirror itself.
// That handler is a distinct owner of the same condition type — it fires
// when the assembled RunConfig fails to build (e.g. delegate clients without
// an OIDC config) — and its Reason is always the fixed
// mcpv1beta1.ConditionReasonInvalidConfig, so an exact match reliably
// identifies it regardless of the mirror's own (source-derived, unbounded)
// Reason values.
func ownedByEmbeddedAuthServerConfigValidation(conditions []metav1.Condition, conditionType string) bool {
	existing := meta.FindStatusCondition(conditions, conditionType)
	return existing != nil && existing.Reason == mcpv1beta1.ConditionReasonInvalidConfig
}

// mirrorInvalidOnMCPServer mirrors the source's Valid=False condition onto the
// MCPServer's ExternalAuthConfigValidated condition. When the source is healed
// (Valid=True or absent), it clears any stale mirror so the condition does not
// outlive its cause — unless the condition currently reflects a different
// owner's failure (handleInvalidEmbeddedAuthServerConfig's
// ConditionReasonInvalidConfig, set when the assembled RunConfig itself is
// invalid, e.g. delegate clients configured without OIDC). Clearing that
// condition here would erase the other owner's terminal failure a step before
// it re-derives the same failure, forcing a fresh LastTransitionTime on every
// reconcile even though nothing changed. Returns (true, err) when a False
// mirror was written so the caller can mark Phase=Failed; (false, nil)
// otherwise.
//
// See package-level Status Condition Parity comment for the #5347 motivation.
func mirrorInvalidOnMCPServer(
	m *mcpv1beta1.MCPServer,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (bool, error) {
	mirrored := mirroredExternalAuthConfigInvalid(externalAuthConfig)
	if mirrored == nil {
		if !ownedByEmbeddedAuthServerConfigValidation(m.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated) {
			meta.RemoveStatusCondition(&m.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
		}
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
// a stale False. It does NOT clear the condition when it currently reflects a
// different owner's failure (handleInvalidEmbeddedAuthServerConfig's
// ConditionReasonInvalidConfig, set when the assembled RunConfig itself is
// invalid, e.g. delegate clients configured without OIDC): removing it here
// would erase that owner's terminal failure a step before it gets re-derived
// unchanged, forcing setMCPRemoteProxyExternalAuthConfigValidCondition to
// treat it as new and stamp a fresh LastTransitionTime on every reconcile
// even though nothing changed. Returns (true, err) when a False mirror was
// written so the caller can short-circuit; (false, nil) otherwise.
func mirrorInvalidOnRemoteProxy(
	proxy *mcpv1beta1.MCPRemoteProxy,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (bool, error) {
	mirrored := mirroredExternalAuthConfigInvalid(externalAuthConfig)
	if mirrored == nil {
		condType := mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated
		if !ownedByEmbeddedAuthServerConfigValidation(proxy.Status.Conditions, condType) {
			meta.RemoveStatusCondition(&proxy.Status.Conditions, condType)
		}
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
