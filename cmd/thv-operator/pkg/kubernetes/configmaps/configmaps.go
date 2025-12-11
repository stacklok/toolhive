package configmaps

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Client provides convenience methods for working with Kubernetes ConfigMaps.
type Client struct {
	client client.Client
	scheme *runtime.Scheme
}

// NewClient creates a new configmaps Client instance.
// The scheme is required for operations that need to set owner references.
func NewClient(c client.Client, scheme *runtime.Scheme) *Client {
	return &Client{
		client: c,
		scheme: scheme,
	}
}

// Get retrieves a Kubernetes ConfigMap by name and namespace.
// Returns the configmap if found, or an error if not found or on failure.
func (c *Client) Get(ctx context.Context, name, namespace string) (*corev1.ConfigMap, error) {
	configMap := &corev1.ConfigMap{}
	err := c.client.Get(ctx, client.ObjectKey{
		Name:      name,
		Namespace: namespace,
	}, configMap)

	if err != nil {
		return nil, fmt.Errorf("failed to get configmap %s in namespace %s: %w", name, namespace, err)
	}

	return configMap, nil
}

// GetValue retrieves a specific key's value from a Kubernetes ConfigMap.
// Uses a ConfigMapKeySelector to identify the configmap name and key.
// Returns the value as a string, or an error if the configmap or key is not found.
func (c *Client) GetValue(ctx context.Context, namespace string, configMapRef corev1.ConfigMapKeySelector) (string, error) {
	configMap, err := c.Get(ctx, configMapRef.Name, namespace)
	if err != nil {
		return "", err
	}

	value, exists := configMap.Data[configMapRef.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in configmap %s", configMapRef.Key, configMapRef.Name)
	}

	return value, nil
}

// UpsertWithOwnerReference creates or updates a Kubernetes ConfigMap with an owner reference.
// The owner reference ensures the configmap is garbage collected when the owner is deleted.
// Uses retry logic to handle conflicts from concurrent modifications.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) UpsertWithOwnerReference(
	ctx context.Context,
	configMap *corev1.ConfigMap,
	owner client.Object,
) (controllerutil.OperationResult, error) {
	return c.upsert(ctx, configMap, owner)
}

// Upsert creates or updates a Kubernetes ConfigMap without an owner reference.
// Uses retry logic to handle conflicts from concurrent modifications.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) Upsert(ctx context.Context, configMap *corev1.ConfigMap) (controllerutil.OperationResult, error) {
	return c.upsert(ctx, configMap, nil)
}

// upsert creates or updates a Kubernetes ConfigMap using retry logic for conflict handling.
// If owner is provided, sets a controller reference to establish ownership.
// This ensures the configmap is garbage collected when the owner is deleted.
// Uses controllerutil.CreateOrUpdate with retry.RetryOnConflict for safe concurrent access.
// Returns the operation result (Created, Updated, or Unchanged) and any error.
func (c *Client) upsert(
	ctx context.Context,
	configMap *corev1.ConfigMap,
	owner client.Object,
) (controllerutil.OperationResult, error) {
	// Store the desired state before calling CreateOrUpdate.
	// This is necessary because CreateOrUpdate first fetches the existing object from the API server
	// and overwrites the object we pass in. Any values we set on the object (other than Name/Namespace)
	// would be lost. By storing them here, we can apply them in the mutate function after the fetch.
	// See: https://pkg.go.dev/sigs.k8s.io/controller-runtime/pkg/controller/controllerutil#CreateOrUpdate
	desiredData := configMap.Data
	desiredBinaryData := configMap.BinaryData
	desiredLabels := configMap.Labels
	desiredAnnotations := configMap.Annotations

	// Create a configmap object with only Name and Namespace set.
	// CreateOrUpdate requires this minimal object - it will fetch the full object from the API server.
	existing := &corev1.ConfigMap{}
	existing.Name = configMap.Name
	existing.Namespace = configMap.Namespace

	var operationResult controllerutil.OperationResult

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		result, err := controllerutil.CreateOrUpdate(ctx, c.client, existing, func() error {
			// Set the desired state
			existing.Data = desiredData
			existing.BinaryData = desiredBinaryData
			existing.Labels = desiredLabels
			existing.Annotations = desiredAnnotations

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
		return controllerutil.OperationResultNone, fmt.Errorf("failed to upsert configmap %s in namespace %s: %w",
			configMap.Name, configMap.Namespace, err)
	}

	return operationResult, nil
}
