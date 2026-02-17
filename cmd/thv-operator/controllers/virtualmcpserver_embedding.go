// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
		// Clean up any previously-owned inline EmbeddingServer when switching to ref mode.
		// Without this, the old inline EmbeddingServer persists and consumes resources.
		if vmcp.Spec.EmbeddingServerRef != nil {
			if err := r.cleanupOwnedEmbeddingServer(ctx, vmcp); err != nil {
				return err
			}
		}
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
			r.Recorder.Eventf(vmcp, corev1.EventTypeNormal, "EmbeddingServerCreated",
				"EmbeddingServer %s created successfully", embeddingName)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("failed to get EmbeddingServer %s: %w", embeddingName, err)
	}

	// EmbeddingServer exists - check if spec needs updating
	if embeddingServerSpecNeedsUpdate(existing, vmcp.Spec.EmbeddingServer) {
		existing.Spec = *vmcp.Spec.EmbeddingServer
		ctxLogger.Info("Updating EmbeddingServer spec", "name", embeddingName)
		if err := r.Update(ctx, existing); err != nil {
			return fmt.Errorf("failed to update EmbeddingServer: %w", err)
		}
		if r.Recorder != nil {
			r.Recorder.Eventf(vmcp, corev1.EventTypeNormal, "EmbeddingServerUpdated",
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
			return false, "", nil // Informer cache may not have caught up yet
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

// embeddingServerSpecNeedsUpdate returns true if the desired spec differs from
// the existing EmbeddingServer spec. Uses field-by-field comparison instead of
// reflect.DeepEqual to avoid spurious updates caused by Kubernetes defaulting
// (e.g., imagePullPolicy, port, replicas) and nil-vs-empty differences in
// runtime.RawExtension fields like PodTemplateSpec.
//
// IMPORTANT: When adding new fields to EmbeddingServerSpec, you must add a
// corresponding comparison here, otherwise changes to the new field will not
// trigger an update of the inline EmbeddingServer CR.
func embeddingServerSpecNeedsUpdate(existing *mcpv1alpha1.EmbeddingServer, desired *mcpv1alpha1.EmbeddingServerSpec) bool {
	e := &existing.Spec

	if e.Model != desired.Model {
		return true
	}
	if e.Image != desired.Image {
		return true
	}
	if e.ImagePullPolicy != desired.ImagePullPolicy {
		return true
	}
	if e.Port != desired.Port {
		return true
	}
	if !stringSlicesEqual(e.Args, desired.Args) {
		return true
	}
	if !envVarSlicesEqual(e.Env, desired.Env) {
		return true
	}
	if e.Resources != desired.Resources {
		return true
	}
	if !secretKeyRefEqual(e.HFTokenSecretRef, desired.HFTokenSecretRef) {
		return true
	}
	if !modelCacheEqual(e.ModelCache, desired.ModelCache) {
		return true
	}
	if !rawExtensionEqual(e.PodTemplateSpec, desired.PodTemplateSpec) {
		return true
	}
	if !embeddingResourceOverridesEqual(e.ResourceOverrides, desired.ResourceOverrides) {
		return true
	}
	if !int32PtrEqual(e.Replicas, desired.Replicas) {
		return true
	}

	return false
}

// embeddingServerName returns the name for an auto-deployed EmbeddingServer.
func embeddingServerName(vmcpName string) string {
	return fmt.Sprintf("%s-embedding", vmcpName)
}

// labelsForEmbeddingServer returns labels for an auto-deployed EmbeddingServer.
func labelsForEmbeddingServer(vmcpName string) map[string]string {
	return map[string]string{
		"toolhive.stacklok.dev/component":          "embedding-server",
		"toolhive.stacklok.dev/virtual-mcp-server": vmcpName,
		"toolhive.stacklok.dev/managed-by":         "toolhive-operator",
	}
}

// embeddingServerRequeueInterval returns the appropriate requeue interval when
// waiting for an EmbeddingServer to become ready.
func (*VirtualMCPServerReconciler) embeddingServerRequeueInterval(
	vmcp *mcpv1alpha1.VirtualMCPServer,
) time.Duration {
	if vmcp.Spec.EmbeddingServer != nil {
		return 10 * time.Second // Inline: Owns() handles primary path
	}
	return 30 * time.Second // Ref: Watches() may be slower
}

// cleanupOwnedEmbeddingServer deletes any previously-owned inline EmbeddingServer
// for this VirtualMCPServer. This handles the transition from inline to ref mode,
// preventing orphaned EmbeddingServers from consuming resources (e.g., GPU/memory).
func (r *VirtualMCPServerReconciler) cleanupOwnedEmbeddingServer(
	ctx context.Context,
	vmcp *mcpv1alpha1.VirtualMCPServer,
) error {
	ctxLogger := log.FromContext(ctx)
	inlineName := embeddingServerName(vmcp.Name)

	existing := &mcpv1alpha1.EmbeddingServer{}
	err := r.Get(ctx, types.NamespacedName{Name: inlineName, Namespace: vmcp.Namespace}, existing)
	if errors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to check for owned EmbeddingServer %s: %w", inlineName, err)
	}

	// Only delete if this VirtualMCPServer owns it (via controller reference)
	for _, ref := range existing.OwnerReferences {
		if ref.UID == vmcp.UID {
			ctxLogger.Info("Cleaning up owned inline EmbeddingServer after switch to ref mode",
				"name", inlineName)
			if err := r.Delete(ctx, existing); err != nil && !errors.IsNotFound(err) {
				return fmt.Errorf("failed to delete owned EmbeddingServer %s: %w", inlineName, err)
			}
			if r.Recorder != nil {
				r.Recorder.Eventf(vmcp, corev1.EventTypeNormal, "EmbeddingServerCleaned",
					"Removed inline EmbeddingServer %s after switch to ref mode", inlineName)
			}
			return nil
		}
	}

	return nil
}

// --- Comparison helpers for embeddingServerSpecNeedsUpdate ---

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func envVarSlicesEqual(a, b []mcpv1alpha1.EnvVar) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func secretKeyRefEqual(a, b *mcpv1alpha1.SecretKeyRef) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Name == b.Name && a.Key == b.Key
}

func modelCacheEqual(a, b *mcpv1alpha1.ModelCacheConfig) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Enabled == b.Enabled &&
		ptrStringEqual(a.StorageClassName, b.StorageClassName) &&
		a.Size == b.Size &&
		a.AccessMode == b.AccessMode
}

func ptrStringEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// rawExtensionEqual compares two RawExtension pointers, treating nil and
// empty byte slices as equal to avoid spurious updates from Kubernetes defaulting.
func rawExtensionEqual(a, b *runtime.RawExtension) bool {
	aRaw := rawExtensionBytes(a)
	bRaw := rawExtensionBytes(b)
	return bytes.Equal(aRaw, bRaw)
}

func rawExtensionBytes(ext *runtime.RawExtension) []byte {
	if ext == nil {
		return nil
	}
	return ext.Raw
}

func embeddingResourceOverridesEqual(a, b *mcpv1alpha1.EmbeddingResourceOverrides) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return reflect.DeepEqual(a, b)
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
