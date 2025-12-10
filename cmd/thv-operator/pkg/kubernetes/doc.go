// Package kubernetes provides utilities for working with Kubernetes resources.
//
// This package offers a structured client wrapper for common operations with Kubernetes
// resources, including retrieving and managing secrets, config maps, and other resources.
//
// The package uses a Client type that wraps a controller-runtime client, providing
// convenient methods for resource operations while maintaining proper error handling
// and logging patterns.
//
// Usage:
//
//	import "github.com/stacklok/toolhive/cmd/thv-operator/pkg/kubernetes"
//
//	// Create a client (requires both controller-runtime client and scheme)
//	kubeClient := kubernetes.NewClient(ctrlClient, scheme)
//
//	// Get a secret
//	secret, err := kubeClient.GetSecret(ctx, "my-secret", "default")
//
//	// Get a specific secret value
//	value, err := kubeClient.GetSecretValue(ctx, "default", secretKeySelector)
//
//	// Create a secret (with optional owner reference)
//	secret := &corev1.Secret{
//		ObjectMeta: metav1.ObjectMeta{
//			Name:      "my-secret",
//			Namespace: "default",
//		},
//		Data: map[string][]byte{
//			"key": []byte("value"),
//		},
//	}
//	err := kubeClient.CreateSecret(ctx, secret, ownerObject)
package kubernetes
