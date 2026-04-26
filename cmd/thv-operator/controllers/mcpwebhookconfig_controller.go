// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	mcpv1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
)

const (
	// WebhookConfigFinalizerName is the name of the finalizer for MCPWebhookConfig
	WebhookConfigFinalizerName = "mcpwebhookconfig.toolhive.stacklok.dev/finalizer"

	// webhookConfigRequeueDelay is the delay before requeuing after adding a finalizer
	webhookConfigRequeueDelay = 500 * time.Millisecond

	webhookConfigDeletionRequeueDelay = 30 * time.Second
)

// MCPWebhookConfigReconciler reconciles a MCPWebhookConfig object
type MCPWebhookConfigReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpwebhookconfigs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpwebhookconfigs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpwebhookconfigs/finalizers,verbs=update
// +kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpservers,verbs=get;list;watch;update;patch

// Reconcile is part of the main kubernetes reconciliation loop
func (r *MCPWebhookConfigReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	webhookConfig := &mcpv1beta1.MCPWebhookConfig{}
	err := r.Get(ctx, req.NamespacedName, webhookConfig)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("MCPWebhookConfig resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get MCPWebhookConfig")
		return ctrl.Result{}, err
	}

	if !webhookConfig.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, webhookConfig)
	}

	if !controllerutil.ContainsFinalizer(webhookConfig, WebhookConfigFinalizerName) {
		controllerutil.AddFinalizer(webhookConfig, WebhookConfigFinalizerName)
		if err := r.Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to add finalizer")
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: webhookConfigRequeueDelay}, nil
	}

	if err := ctrlutil.ValidateMCPWebhookConfigSpec(webhookConfig.Spec); err != nil {
		logger.Error(err, "MCPWebhookConfig spec validation failed")
		meta.SetStatusCondition(&webhookConfig.Status.Conditions, metav1.Condition{
			Type:               mcpv1beta1.ConditionTypeValid,
			Status:             metav1.ConditionFalse,
			Reason:             "ValidationFailed",
			Message:            err.Error(),
			ObservedGeneration: webhookConfig.Generation,
		})
		if updateErr := r.Status().Update(ctx, webhookConfig); updateErr != nil {
			logger.Error(updateErr, "Failed to update MCPWebhookConfig status after validation error")
		}
		return ctrl.Result{}, nil
	}

	conditionChanged := meta.SetStatusCondition(&webhookConfig.Status.Conditions, metav1.Condition{
		Type:               mcpv1beta1.ConditionTypeValid,
		Status:             metav1.ConditionTrue,
		Reason:             "ValidationSucceeded",
		Message:            "Spec validation passed",
		ObservedGeneration: webhookConfig.Generation,
	})

	configHash := r.calculateConfigHash(webhookConfig.Spec)
	if webhookConfig.Status.ConfigHash != configHash {
		return r.handleConfigHashChange(ctx, webhookConfig, configHash)
	}

	if conditionChanged {
		if err := r.Status().Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to update MCPWebhookConfig status after condition change")
			return ctrl.Result{}, err
		}
	}

	return r.updateReferencingWorkloads(ctx, webhookConfig)
}

// calculateConfigHash calculates a hash of the MCPWebhookConfig spec
func (*MCPWebhookConfigReconciler) calculateConfigHash(spec mcpv1beta1.MCPWebhookConfigSpec) string {
	return ctrlutil.CalculateConfigHash(spec)
}

// handleConfigHashChange handles the logic when the config hash changes
func (r *MCPWebhookConfigReconciler) handleConfigHashChange(
	ctx context.Context,
	webhookConfig *mcpv1beta1.MCPWebhookConfig,
	configHash string,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("MCPWebhookConfig configuration changed",
		"oldHash", webhookConfig.Status.ConfigHash,
		"newHash", configHash)

	webhookConfig.Status.ConfigHash = configHash
	webhookConfig.Status.ObservedGeneration = webhookConfig.Generation

	referencingServers, err := r.findReferencingMCPServers(ctx, webhookConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		return ctrl.Result{}, fmt.Errorf("failed to find referencing MCPServers: %w", err)
	}

	webhookConfig.Status.ReferencingWorkloads = workloadRefsFromMCPServers(referencingServers)

	if err := r.Status().Update(ctx, webhookConfig); err != nil {
		logger.Error(err, "Failed to update MCPWebhookConfig status")
		return ctrl.Result{}, err
	}

	var updateErrs []error
	for _, server := range referencingServers {
		logger.Info("Triggering reconciliation of MCPServer due to MCPWebhookConfig change",
			"mcpserver", server.Name, "webhookConfig", webhookConfig.Name)

		if server.Annotations == nil {
			server.Annotations = make(map[string]string)
		}
		server.Annotations["toolhive.stacklok.dev/webhookconfig-hash"] = configHash

		if err := r.Update(ctx, &server); err != nil {
			logger.Error(err, "Failed to update MCPServer annotation", "mcpserver", server.Name)
			updateErrs = append(updateErrs, err)
		}
	}

	if err := utilerrors.NewAggregate(updateErrs); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of a MCPWebhookConfig
func (r *MCPWebhookConfigReconciler) handleDeletion(
	ctx context.Context,
	webhookConfig *mcpv1beta1.MCPWebhookConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if controllerutil.ContainsFinalizer(webhookConfig, WebhookConfigFinalizerName) {
		referencingServers, err := r.findReferencingMCPServers(ctx, webhookConfig)
		if err != nil {
			logger.Error(err, "Failed to find referencing MCPServers during deletion")
			return ctrl.Result{}, err
		}

		if len(referencingServers) > 0 {
			refs := workloadRefsFromMCPServers(referencingServers)
			logger.Info("Cannot delete MCPWebhookConfig - still referenced by workloads",
				"webhookConfig", webhookConfig.Name, "referencingWorkloads", refs)

			meta.SetStatusCondition(&webhookConfig.Status.Conditions, metav1.Condition{
				Type:               mcpv1beta1.ConditionTypeDeletionBlocked,
				Status:             metav1.ConditionTrue,
				Reason:             "ReferencedByWorkloads",
				Message:            fmt.Sprintf("Cannot delete: referenced by workloads: %v", refs),
				ObservedGeneration: webhookConfig.Generation,
			})
			webhookConfig.Status.ReferencingWorkloads = refs
			if err := r.Status().Update(ctx, webhookConfig); err != nil {
				logger.Error(err, "Failed to update MCPWebhookConfig status during deletion")
			}

			return ctrl.Result{RequeueAfter: webhookConfigDeletionRequeueDelay}, nil
		}

		controllerutil.RemoveFinalizer(webhookConfig, WebhookConfigFinalizerName)
		if err := r.Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to remove finalizer")
			return ctrl.Result{}, err
		}
		logger.Info("Removed finalizer from MCPWebhookConfig", "webhookConfig", webhookConfig.Name)
	}

	return ctrl.Result{}, nil
}

// findReferencingMCPServers finds all MCPServers that reference the given MCPWebhookConfig
func (r *MCPWebhookConfigReconciler) findReferencingMCPServers(
	ctx context.Context,
	webhookConfig *mcpv1beta1.MCPWebhookConfig,
) ([]mcpv1beta1.MCPServer, error) {
	return ctrlutil.FindReferencingMCPServers(ctx, r.Client, webhookConfig.Namespace, webhookConfig.Name,
		func(server *mcpv1beta1.MCPServer) *string {
			if server.Spec.WebhookConfigRef != nil {
				return &server.Spec.WebhookConfigRef.Name
			}
			return nil
		})
}

// updateReferencingWorkloads updates the list of workloads referencing this config
func (r *MCPWebhookConfigReconciler) updateReferencingWorkloads(
	ctx context.Context,
	webhookConfig *mcpv1beta1.MCPWebhookConfig,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	referencingServers, err := r.findReferencingMCPServers(ctx, webhookConfig)
	if err != nil {
		logger.Error(err, "Failed to find referencing MCPServers")
		return ctrl.Result{}, err
	}

	refs := workloadRefsFromMCPServers(referencingServers)
	if !ctrlutil.WorkloadRefsEqual(webhookConfig.Status.ReferencingWorkloads, refs) {
		webhookConfig.Status.ReferencingWorkloads = refs
		if err := r.Status().Update(ctx, webhookConfig); err != nil {
			logger.Error(err, "Failed to update referencing workloads list")
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func workloadRefsFromMCPServers(servers []mcpv1beta1.MCPServer) []mcpv1beta1.WorkloadReference {
	refs := make([]mcpv1beta1.WorkloadReference, 0, len(servers))
	for _, server := range servers {
		refs = append(refs, mcpv1beta1.WorkloadReference{Kind: mcpv1beta1.WorkloadKindMCPServer, Name: server.Name})
	}
	ctrlutil.SortWorkloadRefs(refs)
	return refs
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPWebhookConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mcpServerHandler := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			server, ok := obj.(*mcpv1beta1.MCPServer)
			if !ok {
				return nil
			}

			seen := make(map[client.ObjectKey]struct{})
			var requests []reconcile.Request

			if server.Spec.WebhookConfigRef != nil {
				nn := client.ObjectKey{
					Name:      server.Spec.WebhookConfigRef.Name,
					Namespace: server.Namespace,
				}
				seen[nn] = struct{}{}
				requests = append(requests, reconcile.Request{NamespacedName: nn})
			}

			webhookConfigList := &mcpv1beta1.MCPWebhookConfigList{}
			if err := r.List(ctx, webhookConfigList, client.InNamespace(server.Namespace)); err != nil {
				log.FromContext(ctx).Error(err, "Failed to list MCPWebhookConfigs for MCPServer watch")
				return requests
			}
			for _, cfg := range webhookConfigList.Items {
				nn := client.ObjectKey{Name: cfg.Name, Namespace: cfg.Namespace}
				if _, already := seen[nn]; already {
					continue
				}
				for _, ref := range cfg.Status.ReferencingWorkloads {
					if ref.Kind == mcpv1beta1.WorkloadKindMCPServer && ref.Name == server.Name {
						requests = append(requests, reconcile.Request{NamespacedName: nn})
						break
					}
				}
			}

			return requests
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1beta1.MCPWebhookConfig{}).
		Watches(&mcpv1beta1.MCPServer{}, mcpServerHandler).
		Complete(r)
}
