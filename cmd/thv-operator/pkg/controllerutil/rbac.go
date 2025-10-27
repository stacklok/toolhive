package controllerutil

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// EnsureRBACResource is a generic helper function to ensure a Kubernetes RBAC resource exists
func EnsureRBACResource(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	resourceType string,
	createResource func() client.Object,
) error {
	current := createResource()
	objectKey := types.NamespacedName{Name: current.GetName(), Namespace: current.GetNamespace()}
	err := c.Get(ctx, objectKey, current)

	if errors.IsNotFound(err) {
		return createRBACResource(ctx, c, scheme, owner, resourceType, createResource)
	} else if err != nil {
		return fmt.Errorf("failed to get %s: %w", resourceType, err)
	}

	return nil
}

// createRBACResource creates a new RBAC resource with owner reference
func createRBACResource(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	owner client.Object,
	resourceType string,
	createResource func() client.Object,
) error {
	ctxLogger := log.FromContext(ctx)
	desired := createResource()
	if err := controllerutil.SetControllerReference(owner, desired, scheme); err != nil {
		ctxLogger.Error(err, "Failed to set controller reference", "resourceType", resourceType)
		return fmt.Errorf("failed to set controller reference for %s: %w", resourceType, err)
	}

	ctxLogger.Info(
		fmt.Sprintf("%s does not exist, creating", resourceType),
		"resourceType", resourceType,
		"name", desired.GetName(),
	)
	if err := c.Create(ctx, desired); err != nil {
		return fmt.Errorf("failed to create %s: %w", resourceType, err)
	}
	ctxLogger.Info(fmt.Sprintf("%s created", resourceType), "resourceType", resourceType, "name", desired.GetName())
	return nil
}
