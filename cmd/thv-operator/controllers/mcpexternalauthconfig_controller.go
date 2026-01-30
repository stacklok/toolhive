// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

const (
	// ExternalAuthConfigFinalizerName is the name of the finalizer for MCPExternalAuthConfig
	ExternalAuthConfigFinalizerName = "mcpexternalauthconfig.toolhive.stacklok.dev/finalizer"

	// externalAuthConfigRequeueDelay is the delay before requeuing after adding a finalizer
	externalAuthConfigRequeueDelay = 500 * time.Millisecond
)

// MCPExternalAuthConfigReconciler reconciles a MCPExternalAuthConfig object
type MCPExternalAuthConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpexternalauthconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPExternalAuthConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the MCPExternalAuthConfig instance
	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	err := r.Get(ctx, req.NamespacedName, externalAuthConfig)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, could have been deleted after reconcile request.
			// Return and don't requeue
			logger.Info("MCPExternalAuthConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		logger.Error(err, "Failed to get MCPExternalAuthConfig")
		return ctrl.Result{}, err
	}

	// Check if the MCPExternalAuthConfig is being deleted
	if !externalAuthConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, externalAuthConfig)
	}

	// Add finalizer if it doesn't exist
	if !controllerutil.ContainsFinalizer(externalAuthConfig, ExternalAuthConfigFinalizerName) {
		controllerutil.AddFinalizer(externalAuthConfig, ExternalAuthConfigFinalizerName)
		if err := r.Update(ctx, externalAuthConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		// Requeue to continue processing after finalizer is added
		return ctrl.Result{RequeueAfter: externalAuthConfigRequeueDelay}, nil
	}

	// Calculate the hash of the current configuration (including referenced Secret values)
	configHash, err := r.calculateConfigHash(ctx, externalAuthConfig)
	if err != nil {
		logger.Error(err, "Failed to calculate config hash")
		return ctrl.Result{}, err
	}

	// Check if the hash has changed
	if externalAuthConfig.Status.ConfigHash != configHash {
		logger.Info("MCPExternalAuthConfig configuration changed",
			"oldHash", externalAuthConfig.Status.ConfigHash,
			"newHash", configHash)

		// Update the status with the new hash
		externalAuthConfig.Status.ConfigHash = configHash
		externalAuthConfig.Status.ObservedGeneration = externalAuthConfig.Generation

		// Find all MCPServers that reference this MCPExternalAuthConfig
		referencingServers, err := r.findReferencingMCPServers(ctx, externalAuthConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers")
			return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
		}

		// Update the status with the list of referencing servers
		serverNames := make([]string, 0, len(referencingServers))
		for _, server := range referencingServers {
			serverNames = append(serverNames, server.Name)
		}
		externalAuthConfig.Status.ReferencingServers = serverNames

		// Update the MCPExternalAuthConfig status
		if err := r.Status().Update(ctx, externalAuthConfig); err != nil {
			logger.Error(err, "Failed to update MCPExternalAuthConfig status")
			return ctrl.Result{}, err
		}

		// Trigger reconciliation of all referencing MCPServers
		for _, server := range referencingServers {
			logger.Info("Triggering reconciliation of MCPServer due to MCPExternalAuthConfig change",
				"mcpserver", server.Name, "externalAuthConfig", externalAuthConfig.Name)

			// Add an annotation to the MCPServer to trigger reconciliation
			if server.Annotations == nil {
				server.Annotations = make(map[string]string)
			}
			server.Annotations["toolhive.stacklok.dev/externalauthconfig-hash"] = configHash

			if err := r.Update(ctx, &server); err != nil {
				logger.Error(err, "Failed to update MCPServer annotation", "mcpserver", server.Name)
				// Continue with other servers even if one fails
			}
		}
	}

	return ctrl.Result{}, nil
}

// calculateConfigHash calculates a hash of the MCPExternalAuthConfig spec including referenced Secret values
// This ensures that changes to Secret values trigger reconciliation
func (r *MCPExternalAuthConfigReconciler) calculateConfigHash(
	ctx context.Context,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
) (string, error) {
	// Start with the base spec hash
	hashString := ctrlutil.CalculateConfigHash(externalAuthConfig.Spec)

	// Include referenced Secret values in the hash for bearer token configs
	if externalAuthConfig.Spec.Type == mcpv1alpha1.ExternalAuthTypeBearerToken &&
		externalAuthConfig.Spec.BearerToken != nil &&
		externalAuthConfig.Spec.BearerToken.TokenSecretRef != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: externalAuthConfig.Namespace,
			Name:      externalAuthConfig.Spec.BearerToken.TokenSecretRef.Name,
		}, &secret); err != nil {
			if errors.IsNotFound(err) {
				// Secret doesn't exist yet, include that in hash
				hashString += ":secret-not-found"
			} else {
				return "", fmt.Errorf("failed to get bearer token secret: %w", err)
			}
		} else {
			// Include the secret value in the hash
			if tokenValue, ok := secret.Data[externalAuthConfig.Spec.BearerToken.TokenSecretRef.Key]; ok {
				hasher := sha256.New()
				hasher.Write(tokenValue)
				hashString += ":" + hex.EncodeToString(hasher.Sum(nil))[:16]
			} else {
				hashString += ":key-not-found"
			}
		}
	}

	// Also include token exchange client secret if present
	if externalAuthConfig.Spec.Type == mcpv1alpha1.ExternalAuthTypeTokenExchange &&
		externalAuthConfig.Spec.TokenExchange != nil &&
		externalAuthConfig.Spec.TokenExchange.ClientSecretRef != nil {
		var secret corev1.Secret
		if err := r.Get(ctx, types.NamespacedName{
			Namespace: externalAuthConfig.Namespace,
			Name:      externalAuthConfig.Spec.TokenExchange.ClientSecretRef.Name,
		}, &secret); err != nil {
			if errors.IsNotFound(err) {
				hashString += ":client-secret-not-found"
			} else {
				return "", fmt.Errorf("failed to get client secret: %w", err)
			}
		} else {
			if secretValue, ok := secret.Data[externalAuthConfig.Spec.TokenExchange.ClientSecretRef.Key]; ok {
				hasher := sha256.New()
				hasher.Write(secretValue)
				hashString += ":" + hex.EncodeToString(hasher.Sum(nil))[:16]
			} else {
				hashString += ":client-secret-key-not-found"
			}
		}
	}

	// Hash the final combined string
	hasher := sha256.New()
	hasher.Write([]byte(hashString))
	return hex.EncodeToString(hasher.Sum(nil))[:16], nil
}

// handleDeletion handles the deletion of a MCPExternalAuthConfig
func (r *MCPExternalAuthConfigReconciler) handleDeletion(
	ctx context.Context,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(externalAuthConfig, ExternalAuthConfigFinalizerName) {
		// Check if any MCPServers are still referencing this MCPExternalAuthConfig
		referencingServers, err := r.findReferencingMCPServers(ctx, externalAuthConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingServers) > 0 {
			// Cannot delete - still referenced
			serverNames := make([]string, 0, len(referencingServers))
			for _, server := range referencingServers {
				serverNames = append(serverNames, server.Name)
			}
			logger.Info("Cannot delete MCPExternalAuthConfig - still referenced by MCPServers",
				"externalAuthConfig", externalAuthConfig.Name, "referencingServers", serverNames)

			// Update status to show it's still referenced
			externalAuthConfig.Status.ReferencingServers = serverNames
			if err := r.Status().Update(ctx, externalAuthConfig); err != nil {
				logger.Error(err, "Failed to update MCPExternalAuthConfig status during deletion")
			}

			// Return an error to prevent deletion
			return ctrl.Result{}, fmt.Errorf("MCPExternalAuthConfig %s is still referenced by MCPServers: %v",
				externalAuthConfig.Name, serverNames)
		}

		// No references, safe to remove finalizer and allow deletion
		controllerutil.RemoveFinalizer(externalAuthConfig, ExternalAuthConfigFinalizerName)
		if err := r.Update(ctx, externalAuthConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPExternalAuthConfig", "externalAuthConfig", externalAuthConfig.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPExternalAuthConfig
func (r *MCPExternalAuthConfigReconciler) findReferencingMCPServers(
	ctx context.Context,
	externalAuthConfig *mcpv1alpha1.MCPExternalAuthConfig,
) ([]mcpv1alpha1.MCPServer, error) {
	return ctrlutil.FindReferencingMCPServers(ctx, r.Client, externalAuthConfig.Namespace, externalAuthConfig.Name,
		func(server *mcpv1alpha1.MCPServer) *string {
			if server.Spec.ExternalAuthConfigRef != nil {
				return &server.Spec.ExternalAuthConfigRef.Name
			}
			return nil
		})
}

// findMCPExternalAuthConfigsReferencingSecret finds all MCPExternalAuthConfigs that reference the given Secret.
// This includes configs that reference the Secret for bearer tokens or token exchange client secrets.
func (r *MCPExternalAuthConfigReconciler) findMCPExternalAuthConfigsReferencingSecret(
	ctx context.Context,
	secret *corev1.Secret,
) ([]mcpv1alpha1.MCPExternalAuthConfig, error) {
	// List all MCPExternalAuthConfigs in the same namespace as the Secret
	externalAuthConfigs := &mcpv1alpha1.MCPExternalAuthConfigList{}
	if err := r.List(ctx, externalAuthConfigs, client.InNamespace(secret.Namespace)); err != nil {
		return nil, fmt.Errorf("failed to list MCPExternalAuthConfigs: %w", err)
	}

	// Filter configs that reference this Secret
	referencingConfigs := make([]mcpv1alpha1.MCPExternalAuthConfig, 0)
	for _, config := range externalAuthConfigs.Items {
		if configReferencesSecret(&config, secret.Name) {
			referencingConfigs = append(referencingConfigs, config)
		}
	}

	return referencingConfigs, nil
}

// configReferencesSecret checks if an MCPExternalAuthConfig references a Secret by name.
// This checks both bearer token and token exchange configurations.
func configReferencesSecret(
	config *mcpv1alpha1.MCPExternalAuthConfig,
	secretName string,
) bool {
	// Check bearer token config
	if config.Spec.Type == mcpv1alpha1.ExternalAuthTypeBearerToken &&
		config.Spec.BearerToken != nil &&
		config.Spec.BearerToken.TokenSecretRef != nil &&
		config.Spec.BearerToken.TokenSecretRef.Name == secretName {
		return true
	}

	// Check token exchange config
	if config.Spec.Type == mcpv1alpha1.ExternalAuthTypeTokenExchange &&
		config.Spec.TokenExchange != nil &&
		config.Spec.TokenExchange.ClientSecretRef != nil &&
		config.Spec.TokenExchange.ClientSecretRef.Name == secretName {
		return true
	}

	return false
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPExternalAuthConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Create a handler that maps MCPExternalAuthConfig changes to MCPServer reconciliation requests
	externalAuthConfigHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			externalAuthConfig, ok := obj.(*mcpv1alpha1.MCPExternalAuthConfig)
			if !ok {
				return nil
			}

			// Find all MCPServers that reference this MCPExternalAuthConfig
			mcpServers, err := r.findReferencingMCPServers(ctx, externalAuthConfig)
			if err != nil {
				log.FromContext(ctx).Error(err, "Failed to find referencing MCPServers")
				return nil
			}

			// Create reconcile requests for each referencing MCPServer
			requests := make([]reconcile.Request, 0, len(mcpServers))
			for _, server := range mcpServers {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      server.Name,
						Namespace: server.Namespace,
					},
				})
			}

			return requests
		},
	)

	// Create a handler that maps Secret changes to MCPExternalAuthConfig reconciliation requests
	secretHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				return nil
			}

			// Find all MCPExternalAuthConfigs that reference this Secret
			configs, err := r.findMCPExternalAuthConfigsReferencingSecret(ctx, secret)
			if err != nil {
				log.FromContext(ctx).Error(err, "Failed to find MCPExternalAuthConfigs referencing Secret")
				return nil
			}

			requests := make([]reconcile.Request, 0, len(configs))
			for _, config := range configs {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      config.Name,
						Namespace: config.Namespace,
					},
				})
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPExternalAuthConfig{}).
		// Watch for MCPServers and reconcile the MCPExternalAuthConfig when they change
		Watches(&mcpv1alpha1.MCPServer{}, externalAuthConfigHandler).
		// Watch for Secrets and reconcile MCPExternalAuthConfigs that reference them
		Watches(&corev1.Secret{}, secretHandler).
		Complete(r)
}

// GetExternalAuthConfigForMCPServer retrieves the MCPExternalAuthConfig referenced by an MCPServer.
// This function is exported for use by the MCPServer controller (Phase 5 integration).
func GetExternalAuthConfigForMCPServer(
	ctx context.Context,
	c client.Client,
	mcpServer *mcpv1alpha1.MCPServer,
) (*mcpv1alpha1.MCPExternalAuthConfig, error) {
	if mcpServer.Spec.ExternalAuthConfigRef == nil {
		// We throw an error because in this case you assume there is a ExternalAuthConfig
		// but there isn't one referenced.
		return nil, fmt.Errorf("MCPServer %s does not reference a MCPExternalAuthConfig", mcpServer.Name)
	}

	externalAuthConfig := &mcpv1alpha1.MCPExternalAuthConfig{}
	err := c.Get(ctx, types.NamespacedName{
		Name:      mcpServer.Spec.ExternalAuthConfigRef.Name,
		Namespace: mcpServer.Namespace, // Same namespace as MCPServer
	}, externalAuthConfig)

	if err != nil {
		if errors.IsNotFound(err) {
			return nil, fmt.Errorf("MCPExternalAuthConfig %s not found in namespace %s",
				mcpServer.Spec.ExternalAuthConfigRef.Name, mcpServer.Namespace)
		}
		return nil, fmt.Errorf("failed to get MCPExternalAuthConfig: %w", err)
	}

	return externalAuthConfig, nil
}
