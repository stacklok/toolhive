// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// isEmbeddingServerReady checks whether the referenced EmbeddingServer
// is running and ready. Returns a non-nil *string with the URL when ready.
// Returns nil if no embedding server is configured (no gate).
// The caller should check if vmcp.Spec.EmbeddingServerRef != nil && result == nil
// to detect the "configured but not ready" case that requires requeue.
func (r *VirtualMCPServerReconciler) isEmbeddingServerReady(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (*string, error) {
	name := embeddingServerNameForVMCP(vmcp)
	if name == "" {
		return nil, nil // No embedding server configured, skip check
	}

	es := &mcpv1alpha1.EmbeddingServer{}
	err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: vmcp.Namespace}, es)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil // Informer cache may not have caught up yet
		}
		return nil, fmt.Errorf("failed to get EmbeddingServer %s: %w", name, err)
	}

	if es.Status.Phase == mcpv1alpha1.EmbeddingServerPhaseRunning && es.Status.ReadyReplicas > 0 {
		url := es.Status.URL
		return &url, nil
	}

	// Propagate failure so the VirtualMCPServer surfaces it instead of staying Pending
	if es.Status.Phase == mcpv1alpha1.EmbeddingServerPhaseFailed {
		return nil, fmt.Errorf("EmbeddingServer %s has failed", name)
	}

	return nil, nil // Not ready yet
}

// resolveEmbeddingServiceURL looks up the referenced EmbeddingServer CR
// and returns its Status.URL, which is the full base URL including scheme, host, and port
// (e.g., http://name.namespace.svc.cluster.local:8080).
// Returns empty string if no embedding server is configured.
func (r *VirtualMCPServerReconciler) resolveEmbeddingServiceURL(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) (string, error) {
	name := embeddingServerNameForVMCP(vmcp)
	if name == "" {
		return "", nil
	}

	es := &mcpv1alpha1.EmbeddingServer{}
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: vmcp.Namespace}, es); err != nil {
		return "", fmt.Errorf("failed to get EmbeddingServer %s: %w", name, err)
	}

	return es.Status.URL, nil
}

// embeddingServerNameForVMCP resolves the EmbeddingServer name for a VirtualMCPServer.
// Returns empty string if no embedding server is configured.
func embeddingServerNameForVMCP(vmcp *mcpv1alpha1.VirtualMCPServer) string {
	if vmcp.Spec.EmbeddingServerRef != nil {
		return vmcp.Spec.EmbeddingServerRef.Name
	}
	return ""
}
