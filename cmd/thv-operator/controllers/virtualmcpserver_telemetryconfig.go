// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/virtualmcpserverstatus"
)

// handleTelemetryConfig validates and tracks the hash of the referenced MCPTelemetryConfig.
// It sets the TelemetryConfigRefValidated condition and triggers reconciliation when
// the telemetry configuration changes.
// Uses the batched statusManager pattern instead of direct r.Status().Update().
func (r *VirtualMCPServerReconciler) handleTelemetryConfig(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
	statusManager virtualmcpserverstatus.StatusManager,
) error {
	ctxLogger := log.FromContext(ctx)

	if vmcp.Spec.TelemetryConfigRef == nil {
		// No MCPTelemetryConfig referenced, clear any stored hash
		if vmcp.Status.TelemetryConfigHash != "" {
			statusManager.SetTelemetryConfigHash("")
		}
		return nil
	}

	// Get the referenced MCPTelemetryConfig
	telemetryConfig, err := ctrlutil.GetTelemetryConfigForVirtualMCPServer(ctx, r.Client, vmcp)
	if err != nil {
		// Transient API error (not a NotFound)
		statusManager.SetTelemetryConfigRefValidatedCondition(
			mcpv1alpha1.ConditionReasonVirtualMCPServerTelemetryConfigRefFetchError,
			err.Error(),
			metav1.ConditionFalse,
		)
		return err
	}

	if telemetryConfig == nil {
		// Resource genuinely does not exist
		statusManager.SetTelemetryConfigRefValidatedCondition(
			mcpv1alpha1.ConditionReasonVirtualMCPServerTelemetryConfigRefNotFound,
			fmt.Sprintf("MCPTelemetryConfig %s not found", vmcp.Spec.TelemetryConfigRef.Name),
			metav1.ConditionFalse,
		)
		return fmt.Errorf("MCPTelemetryConfig %s not found", vmcp.Spec.TelemetryConfigRef.Name)
	}

	// Validate that the MCPTelemetryConfig is valid (has Valid=True condition)
	if err := telemetryConfig.Validate(); err != nil {
		statusManager.SetTelemetryConfigRefValidatedCondition(
			mcpv1alpha1.ConditionReasonVirtualMCPServerTelemetryConfigRefInvalid,
			fmt.Sprintf("MCPTelemetryConfig %s is invalid: %v", vmcp.Spec.TelemetryConfigRef.Name, err),
			metav1.ConditionFalse,
		)
		return fmt.Errorf("MCPTelemetryConfig %s is invalid: %w", vmcp.Spec.TelemetryConfigRef.Name, err)
	}

	// Set valid condition
	statusManager.SetTelemetryConfigRefValidatedCondition(
		mcpv1alpha1.ConditionReasonVirtualMCPServerTelemetryConfigRefValid,
		fmt.Sprintf("MCPTelemetryConfig %s is valid", vmcp.Spec.TelemetryConfigRef.Name),
		metav1.ConditionTrue,
	)

	// Check if the MCPTelemetryConfig hash has changed
	if vmcp.Status.TelemetryConfigHash != telemetryConfig.Status.ConfigHash {
		ctxLogger.Info("MCPTelemetryConfig has changed, updating VirtualMCPServer",
			"vmcp", vmcp.Name,
			"telemetryConfig", telemetryConfig.Name,
			"oldHash", vmcp.Status.TelemetryConfigHash,
			"newHash", telemetryConfig.Status.ConfigHash)

		statusManager.SetTelemetryConfigHash(telemetryConfig.Status.ConfigHash)
	}

	return nil
}

// mapTelemetryConfigToVirtualMCPServer maps MCPTelemetryConfig changes to VirtualMCPServer reconciliation requests.
// Used by SetupWithManager to watch MCPTelemetryConfig resources.
func (r *VirtualMCPServerReconciler) mapTelemetryConfigToVirtualMCPServer(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	telemetryConfig, ok := obj.(*mcpv1alpha1.MCPTelemetryConfig)
	if !ok {
		return nil
	}

	vmcpList := &mcpv1alpha1.VirtualMCPServerList{}
	if err := r.List(ctx, vmcpList, client.InNamespace(telemetryConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list VirtualMCPServers for MCPTelemetryConfig watch")
		return nil
	}

	var requests []reconcile.Request
	for _, vmcp := range vmcpList.Items {
		if vmcp.Spec.TelemetryConfigRef != nil &&
			vmcp.Spec.TelemetryConfigRef.Name == telemetryConfig.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      vmcp.Name,
					Namespace: vmcp.Namespace,
				},
			})
		}
	}

	return requests
}
