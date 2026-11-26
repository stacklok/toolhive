// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	stderrors "errors"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/externalauthsupport"
)

// Status Condition Parity machinery for #5347. The probe reads a referenced
// MCPExternalAuthConfig's Valid condition; the per-consumer mirror functions
// transcribe its reason and message (the latter behind mirroredMessagePrefix)
// onto the consumer CR's parallel condition.
// Without this, an invalid MCPExternalAuthConfig (for example a malformed obo
// config) surfaces its reason only on the referenced resource and the consumer
// status reports only the generic dispatch failure.

// fallbackInvalidReason is substituted when a source surfaces Valid=False with
// an empty Reason. metav1.Condition requires Reason to be non-empty and the
// apiserver rejects empty-Reason patches, so the mirror cannot copy an empty
// Reason verbatim without trapping the consumer in a noisy reconcile loop.
const fallbackInvalidReason = "InvalidExternalAuthConfig"

// mirroredMessagePrefix starts the Message of every condition the mirror
// writes, marking it as mirrored rather than locally owned. A source can
// surface the same Reason a consumer uses for its own validation failures (a
// malformed obo config is Valid=False / InvalidConfig), so the Reason alone
// cannot tell the two owners apart. No local writer of InvalidConfig,
// InvalidCABundle or MultiUpstreamNotSupported may start its message with this prefix;
// TestMirroredMessagePrefix_NotUsedByLocalMessages checks the obo
// incoming-auth message and the shared CA bundle format
// (ctrlutil.InvalidCABundleMessage).
const mirroredMessagePrefix = "Referenced MCPExternalAuthConfig "

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

// externalAuthReasonFromError returns a stable condition reason for typed
// external-auth failures. Source-level invalidity keeps its mirrored reason;
// consumer/type incompatibility uses the shared UnsupportedAuthType taxonomy.
func externalAuthReasonFromError(err error) string {
	if reason := mirroredReasonFromError(err); reason != "" {
		return reason
	}

	var unsupported *externalauthsupport.UnsupportedTypeError
	if stderrors.As(err, &unsupported) {
		return mcpv1beta1.ConditionReasonUnsupportedAuthType
	}
	return ""
}

// isTerminalExternalAuthError reports whether err is a consumer/type
// incompatibility: the referenced MCPExternalAuthConfig has a type the consumer
// does not implement. That can only heal after the consumer spec or the
// referenced config changes. Both are watched by the consumer controllers, so
// rate-limited retries of the same generation add noise but cannot make
// progress. A source that reports Valid=False is not covered here and keeps
// returning its error to the caller.
func isTerminalExternalAuthError(err error) bool {
	var unsupported *externalauthsupport.UnsupportedTypeError
	return stderrors.As(err, &unsupported)
}

// externalAuthValidCondition returns the reason and message of a True
// ExternalAuthConfigValidated condition on a backend resource (MCPServer or
// MCPRemoteProxy). validReason is the consumer's own "valid" reason.
//
// A type the backend accepts but does not apply itself gets a distinct reason
// and a message that says so. The condition stays True, because the reference
// is correct for a backend of a VirtualMCPServer and failing it would take
// discovered mode down; but a backend that is used on its own would otherwise
// read as protected by a config its proxy never applies.
func externalAuthValidCondition(
	consumer externalauthsupport.Consumer,
	validReason, configName string,
	authType mcpv1beta1.ExternalAuthType,
) (reason, message string) {
	if externalauthsupport.AppliedByVirtualMCPServer(consumer, authType) {
		return mcpv1beta1.ConditionReasonAppliedByVirtualMCPServer, fmt.Sprintf(
			"MCPExternalAuthConfig '%s' is valid, but %s does not apply type %q itself: "+
				"it takes effect only when a VirtualMCPServer discovers this backend",
			configName, consumer, authType)
	}
	return validReason, fmt.Sprintf("MCPExternalAuthConfig '%s' is valid", configName)
}

// ownedByLocalValidation reports whether conditions' entry for conditionType
// (if any) was set by this operator's own validation rather than mirrored from
// the referenced MCPExternalAuthConfig. See isLocalValidationFailure.
func ownedByLocalValidation(conditions []metav1.Condition, conditionType string) bool {
	return isLocalValidationFailure(meta.FindStatusCondition(conditions, conditionType))
}

// isLocalValidationFailure reports whether cond is a False condition written by
// this operator's own validation of the consumer. Four kinds of owner qualify,
// each with a fixed Reason:
//
//   - ConditionReasonInvalidConfig: handleInvalidEmbeddedAuthServerConfig, set
//     when the assembled RunConfig fails to build (e.g. delegate clients
//     without an OIDC config), and the obo incoming-auth check
//     (ctrlutil.ValidateOBOIncomingAuth).
//   - ConditionReasonInvalidCABundle: the CA bundle check, set when a
//     referenced bundle ConfigMap is missing its key or holds non-PEM content.
//   - ConditionReasonExternalAuthConfigMultiUpstream (the same string as
//     ConditionReasonMCPRemoteProxyExternalAuthConfigMultiUpstream): the
//     single-upstream check, set when an embedded auth server config lists
//     more than one upstream.
//   - ConditionReasonUnsupportedAuthType: the consumer/type check, set when the
//     referenced config's type is not implemented by this consumer kind.
//
// These re-derive the same condition after the mirror runs, so clearing it in
// the mirror would force a fresh LastTransitionTime on every reconcile.
//
// A source can surface the same Reason (a malformed obo config is Valid=False /
// InvalidConfig), so a condition whose Message starts with
// mirroredMessagePrefix is the mirror's whatever its Reason. Treating it as
// local would hold it after the source heals back to an unchanged ConfigHash:
// the mirror would not clear it and the same-hash guard in the
// set*ExternalAuthConfigValidCondition writers would not overwrite it.
func isLocalValidationFailure(cond *metav1.Condition) bool {
	if cond == nil || cond.Status != metav1.ConditionFalse {
		return false
	}
	if strings.HasPrefix(cond.Message, mirroredMessagePrefix) {
		return false
	}
	return cond.Reason == mcpv1beta1.ConditionReasonInvalidConfig ||
		cond.Reason == mcpv1beta1.ConditionReasonInvalidCABundle ||
		cond.Reason == mcpv1beta1.ConditionReasonExternalAuthConfigMultiUpstream ||
		cond.Reason == mcpv1beta1.ConditionReasonUnsupportedAuthType
}

// clearStaleMirror removes conditions' entry for conditionType only when it is
// a failure the mirror wrote on an earlier reconcile, so that failure does not
// outlive its cause once the source has healed. The mirror only ever writes
// Status=False, so a True entry (the consumer's own ExternalAuthConfigValid
// success condition) is never a stale mirror, and neither is a False entry
// that ownedByLocalValidation attributes to another owner. Removing either
// would only have the consumer re-add it a few lines later with a fresh
// LastTransitionTime, so every reconcile would write status, and that write
// would trigger the next reconcile through the consumer's own watch.
func clearStaleMirror(conditions *[]metav1.Condition, conditionType string) {
	existing := meta.FindStatusCondition(*conditions, conditionType)
	if existing == nil || existing.Status != metav1.ConditionFalse ||
		ownedByLocalValidation(*conditions, conditionType) {
		return
	}
	meta.RemoveStatusCondition(conditions, conditionType)
}

// mirroredConditionMessage builds the consumer condition Message for the
// source sourceName whose Valid=False condition carries sourceMessage. Its
// mirroredMessagePrefix is what isLocalValidationFailure keys on.
func mirroredConditionMessage(sourceName, sourceMessage string) string {
	if sourceMessage == "" {
		return fmt.Sprintf("%s'%s' is invalid", mirroredMessagePrefix, sourceName)
	}
	return fmt.Sprintf("%s'%s' is invalid: %s", mirroredMessagePrefix, sourceName, sourceMessage)
}

// mirrorInvalidOnMCPServer mirrors the source's Valid=False condition onto the
// MCPServer's ExternalAuthConfigValidated condition. When the source is healed
// (Valid=True or absent), it clears any stale mirror so the condition does not
// outlive its cause — unless the condition is the healthy True condition or
// currently reflects a different owner's failure (see clearStaleMirror).
// Clearing either here would erase the condition a step before
// handleExternalAuthConfig re-derives it unchanged, forcing a fresh
// LastTransitionTime on every reconcile even though nothing changed. Returns
// (true, err) when a False mirror was written so the caller can mark
// Phase=Failed; (false, nil) otherwise.
//
// See package-level Status Condition Parity comment for the #5347 motivation.
func mirrorInvalidOnMCPServer(
	m *mcpv1beta1.MCPServer,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (bool, error) {
	mirrored := mirroredExternalAuthConfigInvalid(externalAuthConfig)
	if mirrored == nil {
		clearStaleMirror(&m.Status.Conditions, mcpv1beta1.ConditionTypeExternalAuthConfigValidated)
		return false, nil
	}
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeExternalAuthConfigValidated,
		Status:             metav1.ConditionFalse,
		Reason:             mirrored.Reason,
		Message:            mirroredConditionMessage(externalAuthConfig.Name, mirrored.Message),
		ObservedGeneration: m.Generation,
	})
	return true, fmt.Errorf("MCPExternalAuthConfig %s/%s: %w", m.Namespace, externalAuthConfig.Name, mirrored)
}

// mirrorInvalidOnRemoteProxy mirrors the source's Valid=False condition onto
// the MCPRemoteProxy's ExternalAuthConfigValidated condition. When the source
// is healed, it clears any stale mirror defensively — the downstream True
// writer in handleExternalAuthConfig also sets the success reason, but a
// future early return between this site and that writer would otherwise leak
// a stale False. It does NOT clear the condition when it is the healthy True
// condition or currently reflects a different owner's failure (see
// clearStaleMirror): removing it here would erase that condition a step
// before it gets re-derived unchanged, forcing
// setMCPRemoteProxyExternalAuthConfigValidCondition to treat it as new and
// stamp a fresh LastTransitionTime on every reconcile even though nothing
// changed. Returns (true, err) when a False mirror was written so the caller
// can short-circuit; (false, nil) otherwise.
func mirrorInvalidOnRemoteProxy(
	proxy *mcpv1beta1.MCPRemoteProxy,
	externalAuthConfig *mcpv1beta1.MCPExternalAuthConfig,
) (bool, error) {
	mirrored := mirroredExternalAuthConfigInvalid(externalAuthConfig)
	if mirrored == nil {
		clearStaleMirror(&proxy.Status.Conditions, mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated)
		return false, nil
	}
	meta.SetStatusCondition(&proxy.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeMCPRemoteProxyExternalAuthConfigValidated,
		Status:             metav1.ConditionFalse,
		Reason:             mirrored.Reason,
		Message:            mirroredConditionMessage(externalAuthConfig.Name, mirrored.Message),
		ObservedGeneration: proxy.Generation,
	})
	return true, fmt.Errorf("MCPExternalAuthConfig %s/%s: %w", proxy.Namespace, externalAuthConfig.Name, mirrored)
}
