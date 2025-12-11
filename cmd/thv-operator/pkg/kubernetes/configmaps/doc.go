// Package configmaps provides convenience methods for working with Kubernetes ConfigMaps.
//
// This package provides a Client that wraps the controller-runtime client
// with ConfigMap-specific operations including Get, GetValue, and Upsert operations.
//
// Example usage:
//
//	client := configmaps.NewClient(ctrlClient, scheme)
//
//	// Get a ConfigMap
//	cm, err := client.Get(ctx, "my-configmap", "default")
//
//	// Get a specific key's value using ConfigMapKeySelector
//	value, err := client.GetValue(ctx, "default", configMapKeySelector)
//
//	// Upsert a ConfigMap with owner reference
//	result, err := client.UpsertWithOwnerReference(ctx, configMap, ownerObject)
package configmaps
