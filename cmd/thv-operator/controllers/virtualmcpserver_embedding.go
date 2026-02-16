// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// ensureEmbeddingServer ensures the EmbeddingServer CR exists when the
// VirtualMCPServer has an inline embeddingServer spec configured. The EmbeddingServer
// is created with an owner reference so it is cascade-deleted with the parent.
// Reconciliation is re-triggered automatically via the Owns() watch when the
// EmbeddingServer status changes.
//
// When EmbeddingServerRef is used instead, this function is a no-op because
// the referenced EmbeddingServer is not owned by this VirtualMCPServer.
func (r *VirtualMCPServerReconciler) ensureEmbeddingServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	// Skip if no inline embedding server spec is configured.
	// When EmbeddingServerRef is used, the referenced resource is externally managed.
	if vmcp.Spec.EmbeddingServer == nil {
		return nil
	}

	ctxLogger := log.FromContext(ctx)
	embeddingName := embeddingServerName(vmcp.Name)

	// Validate generated name length. EmbeddingServer creates a Service whose name
	// is a DNS label (max 63 characters).
	if len(embeddingName) > 63 {
		return fmt.Errorf(
			"generated EmbeddingServer name %q exceeds 63-character DNS label limit; "+
				"shorten the VirtualMCPServer name", embeddingName)
	}

	// Check if EmbeddingServer already exists
	existing := &mcpv1alpha1.EmbeddingServer{}
	err := r.Get(ctx, types.NamespacedName{Name: embeddingName, Namespace: vmcp.Namespace}, existing)

	if errors.IsNotFound(err) {
		// Create new EmbeddingServer with owner reference
		es := &mcpv1alpha1.EmbeddingServer{
			ObjectMeta: metav1.ObjectMeta{
				Name:      embeddingName,
				Namespace: vmcp.Namespace,
				Labels:    labelsForEmbeddingServer(vmcp.Name),
			},
			Spec: *vmcp.Spec.EmbeddingServer,
		}
		if err := controllerutil.SetControllerReference(vmcp, es, r.Scheme); err != nil {
			return fmt.Errorf("failed to set controller reference on EmbeddingServer: %w", err)
		}

		ctxLogger.Info("Creating EmbeddingServer", "name", embeddingName, "namespace", vmcp.Namespace)
		if err := r.Create(ctx, es); err != nil {
			return fmt.Errorf("failed to create EmbeddingServer: %w", err)
		}

		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, "Normal", "EmbeddingServerCreated",
				"EmbeddingServer %s created successfully", embeddingName)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get EmbeddingServer %s: %w", embeddingName, err)
	}

	// EmbeddingServer exists - check if spec needs updating
	if embeddingServerSpecChanged(existing, vmcp.Spec.EmbeddingServer) {
		existing.Spec = *vmcp.Spec.EmbeddingServer
		ctxLogger.Info("Updating EmbeddingServer spec", "name", embeddingName)
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update EmbeddingServer: %w", err)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, "Normal", "EmbeddingServerUpdated",
				"EmbeddingServer %s updated", embeddingName)
		}
	}

	return nil
}

// isEmbeddingServerReady checks whether the EmbeddingServer (inline or referenced)
// is running and ready. If no embedding server is configured, returns true (no gate).
func (r *VirtualMCPServerReconciler) isEmbeddingServerReady(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (bool, string, error) {
	name := embeddingServerNameForVMCP(vmcp)
	if name == "" {
		return true, "", nil // No embedding server configured, skip check
	}

	es := &mcpv1alpha1.EmbeddingServer{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: vmcp.Namespace}, es)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, "", nil // Not yet created
		}
		return false, "", fmt.Errorf("failed to get EmbeddingServer %s: %w", name, err)
	}

	if es.Status.Phase == mcpv1alpha1.EmbeddingServerPhaseRunning && es.Status.ReadyReplicas > 0 {
		return true, es.Status.URL, nil
	}

	// Propagate failure so the VirtualMCPServer surfaces it instead of staying Pending
	if es.Status.Phase == mcpv1alpha1.EmbeddingServerPhaseFailed {
		return false, "", fmt.Errorf("EmbeddingServer %s has failed", name)
	}

	return false, "", nil
}

// embeddingServerNameForVMCP resolves the EmbeddingServer name for a VirtualMCPServer,
// regardless of whether it uses inline or reference mode.
// Returns empty string if no embedding server is configured.
func embeddingServerNameForVMCP(vmcp *mcpv1alpha1.VirtualMCPServer) string {
	if vmcp.Spec.EmbeddingServer != nil {
		return embeddingServerName(vmcp.Name)
	}
	if vmcp.Spec.EmbeddingServerRef != nil {
		return vmcp.Spec.EmbeddingServerRef.Name
	}
	return ""
}

// embeddingServerSpecChanged returns true if the desired spec differs from
// the existing EmbeddingServer spec. Uses DeepEqual to ensure all fields
// (Model, Image, Port, HFTokenSecretRef, Args, Env, Resources, Replicas, etc.)
// are compared so that spec changes are never silently ignored.
func embeddingServerSpecChanged(existing *mcpv1alpha1.EmbeddingServer, desired *mcpv1alpha1.EmbeddingServerSpec) bool {
	return !reflect.DeepEqual(existing.Spec, *desired)
}

// embeddingServerName returns the name for an auto-deployed EmbeddingServer.
func embeddingServerName(vmcpName string) string {
	return fmt.Sprintf("%s-embedding", vmcpName)
}

// labelsForEmbeddingServer returns labels for an auto-deployed EmbeddingServer.
func labelsForEmbeddingServer(vmcpName string) map[string]string {
	return map[string]string{
		"toolhive.stacklok.io/component":          "embedding-server",
		"toolhive.stacklok.io/virtual-mcp-server": vmcpName,
		"toolhive.stacklok.io/managed-by":         "toolhive-operator",
	}
}
