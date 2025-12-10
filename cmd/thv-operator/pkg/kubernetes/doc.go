// Package kubernetes provides utilities for working with Kubernetes resources.
//
// This package provides a unified Client that composes domain-specific clients
// for different Kubernetes resource types. Each sub-client handles operations
// for its specific resource type.
//
// Sub-packages:
//
//   - secrets: Operations for Kubernetes Secrets (Get, GetValue, Upsert)
//
// Example usage:
//
//	import "github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes"
//
//	// Create the unified client
//	kubeClient := kubernetes.NewClient(ctrlClient, scheme)
//
//	// Access secrets operations via the Secrets field
//	value, err := kubeClient.Secrets.GetValue(ctx, "default", secretKeySelector)
//
//	// Upsert a secret with owner reference
//	result, err := kubeClient.Secrets.UpsertWithOwnerReference(ctx, secret, ownerObject)
package kubernetes
