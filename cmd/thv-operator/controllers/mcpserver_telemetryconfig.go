// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// handleTelemetryConfig validates and tracks the hash of the referenced MCPTelemetryConfig.
// It updates the MCPServer status when the telemetry configuration changes.
func (r *MCPServerReconciler) handleTelemetryConfig(ctx context.Context, m *mcpv1alpha1.MCPServer) error {
	ctxLogger := log.FromContext(ctx)

	if m.Spec.TelemetryConfigRef == nil {
		// No MCPTelemetryConfig referenced, clear any stored hash
		if m.Status.TelemetryConfigHash != "" {
			m.Status.TelemetryConfigHash = ""
			if err := r.Status().Update(ctx, m); err != nil {
				return fmt.Errorf("failed to clear MCPTelemetryConfig hash from status: %w", err)
			}
		}
		return nil
	}

	// Get the referenced MCPTelemetryConfig
	telemetryConfig, err := getTelemetryConfigForMCPServer(ctx, r.Client, m)
	if err != nil {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTelemetryConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonTelemetryConfigRefNotFound,
			Message:            err.Error(),
			ObservedGeneration: m.Generation,
		})
		return err
	}

	if telemetryConfig == nil {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTelemetryConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonTelemetryConfigRefNotFound,
			Message:            fmt.Sprintf("MCPTelemetryConfig %s not found", m.Spec.TelemetryConfigRef.Name),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf("MCPTelemetryConfig %s not found", m.Spec.TelemetryConfigRef.Name)
	}

	// Validate that the MCPTelemetryConfig is valid (has Valid=True condition)
	if err := telemetryConfig.Validate(); err != nil {
		meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionTelemetryConfigRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonTelemetryConfigRefInvalid,
			Message:            fmt.Sprintf("MCPTelemetryConfig %s is invalid: %v", m.Spec.TelemetryConfigRef.Name, err),
			ObservedGeneration: m.Generation,
		})
		return fmt.Errorf("MCPTelemetryConfig %s is invalid: %w", m.Spec.TelemetryConfigRef.Name, err)
	}

	// Set condition to valid
	meta.SetStatusCondition(&m.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionTelemetryConfigRefValidated,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonTelemetryConfigRefValid,
		Message:            fmt.Sprintf("MCPTelemetryConfig %s is valid", m.Spec.TelemetryConfigRef.Name),
		ObservedGeneration: m.Generation,
	})

	// Check if the MCPTelemetryConfig hash has changed
	if m.Status.TelemetryConfigHash != telemetryConfig.Status.ConfigHash {
		ctxLogger.Info("MCPTelemetryConfig has changed, updating MCPServer",
			"mcpserver", m.Name,
			"telemetryConfig", telemetryConfig.Name,
			"oldHash", m.Status.TelemetryConfigHash,
			"newHash", telemetryConfig.Status.ConfigHash)

		m.Status.TelemetryConfigHash = telemetryConfig.Status.ConfigHash
		if err := r.Status().Update(ctx, m); err != nil {
			return fmt.Errorf("failed to update MCPTelemetryConfig hash in status: %w", err)
		}
	}

	return nil
}

// getTelemetryConfigForMCPServer fetches the MCPTelemetryConfig referenced by an MCPServer.
func getTelemetryConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	m *mcpv1alpha1.MCPServer,
) (*mcpv1alpha1.MCPTelemetryConfig, error) {
	if m.Spec.TelemetryConfigRef == nil {
		return nil, nil
	}

	telemetryConfig := &mcpv1alpha1.MCPTelemetryConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      m.Spec.TelemetryConfigRef.Name,
		Namespace: m.Namespace,
	}, telemetryConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to get MCPTelemetryConfig %s: %w", m.Spec.TelemetryConfigRef.Name, err)
	}

	return telemetryConfig, nil
}

// mapTelemetryConfigToServers maps MCPTelemetryConfig changes to MCPServer reconciliation requests.
// Used by SetupWithManager to watch MCPTelemetryConfig resources.
func (r *MCPServerReconciler) mapTelemetryConfigToServers(
	ctx context.Context, obj client.Object,
) []reconcile.Request {
	telemetryConfig, ok := obj.(*mcpv1alpha1.MCPTelemetryConfig)
	if !ok {
		return nil
	}

	mcpServerList := &mcpv1alpha1.MCPServerList{}
	if err := r.List(ctx, mcpServerList, client.InNamespace(telemetryConfig.Namespace)); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list MCPServers for MCPTelemetryConfig watch")
		return nil
	}

	var requests []reconcile.Request
	for _, server := range mcpServerList.Items {
		if server.Spec.TelemetryConfigRef != nil &&
			server.Spec.TelemetryConfigRef.Name == telemetryConfig.Name {
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      server.Name,
					Namespace: server.Namespace,
				},
			})
		}
	}

	return requests
}
