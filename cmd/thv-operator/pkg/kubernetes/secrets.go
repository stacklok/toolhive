package kubernetes

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// GetSecret retrieves a Kubernetes Secret by name and namespace.
// Returns the secret if found, or an error if not found or on failure.
func (c *Client) GetSecret(ctx context.Context, name, namespace string) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := c.client.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, secret)

	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s in namespace %s: %w", name, namespace, err)
	}

	return secret, nil
}

// GetSecretValue retrieves a specific key's value from a Kubernetes Secret.
// Uses a SecretKeySelector to identify the secret name and key.
// Returns the value as a string, or an error if the secret or key is not found.
func (c *Client) GetSecretValue(ctx context.Context, namespace string, secretRef corev1.SecretKeySelector) (string, error) {
	secret, err := c.GetSecret(ctx, secretRef.Name, namespace)
	if err != nil {
		return "", err
	}

	value, exists := secret.Data[secretRef.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in secret %s", secretRef.Key, secretRef.Name)
	}

	return string(value), nil
}

// UpsertSecretWithOwnerReference creates or updates a Kubernetes Secret with an owner reference.
// The owner reference ensures the secret is garbage collected when the owner is deleted.
// Uses retry logic to handle conflicts from concurrent modifications.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) UpsertSecretWithOwnerReference(
	ctx context.Context,
	secret *corev1.Secret,
	owner client.Object,
) (controllerutil.OperationResult, error) {
	return c.upsertSecret(ctx, secret, owner)
}

// UpsertSecret creates or updates a Kubernetes Secret without an owner reference.
// Uses retry logic to handle conflicts from concurrent modifications.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) UpsertSecret(ctx context.Context, secret *corev1.Secret) (controllerutil.OperationResult, error) {
	return c.upsertSecret(ctx, secret, nil)
}

// upsertSecret creates or updates a Kubernetes Secret using retry logic for conflict handling.
// If owner is provided, sets a controller reference to establish ownership.
// This ensures the secret is garbage collected when the owner is deleted.
// Uses controllerutil.CreateOrUpdate with retry.RetryOnConflict for safe concurrent access.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) upsertSecret(
	ctx context.Context,
	secret *corev1.Secret,
	owner client.Object,
) (controllerutil.OperationResult, error) {
	// Store the desired state before calling CreateOrUpdate.
	// This is necessary because CreateOrUpdate first fetches the existing object from the API server
	// and overwrites the object we pass in. Any values we set on the object (other than Name/Namespace)
	// would be lost. By storing them here, we can apply them in the mutate function after the fetch.
	// See: https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/controller/controllerutil#CreateOrUpdate
	desiredData := secret.Data
	desiredLabels := secret.Labels
	desiredAnnotations := secret.Annotations
	desiredType := secret.Type

	// Create a secret object with only Name and Namespace set.
	// CreateOrUpdate requires this minimal object - it will fetch the full object from the API server.
	existing := &corev1.Secret{}
	existing.Name = secret.Name
	existing.Namespace = secret.Namespace

	var operationResult controllerutil.OperationResult

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result, err := controllerutil.CreateOrUpdate(ctx, c.client, existing, func() error {
			// Set the desired state
			existing.Data = desiredData
			existing.Labels = desiredLabels
			existing.Annotations = desiredAnnotations
			if desiredType != "" {
				existing.Type = desiredType
			}

			// Set owner reference if provided
			if owner != nil {
				if err := controllerutil.SetControllerReference(owner, existing, c.scheme); err != nil {
					return fmt.Errorf("failed to set controller reference: %w", err)
				}
			}

			return nil
		})

		if err != nil {
			return err
		}

		operationResult = result
		return nil
	})

	if err != nil {
		return controllerutil.OperationResultNone, fmt.Errorf("failed to upsert secret %s in namespace %s: %w",
			secret.Name, secret.Namespace, err)
	}

	return operationResult, nil
}
