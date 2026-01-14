// Package controllers contains the reconciliation logic for the MCPEmbedding custom resource.
// It handles the creation, update, and deletion of HuggingFace embedding inference servers in Kubernetes.
package controllers

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	ctrlutil "github.com/stacklok/toolhive/cmd/thv-operator/pkg/controllerutil"
	"github.com/stacklok/toolhive/cmd/thv-operator/pkg/validation"
)

// MCPEmbeddingReconciler reconciles a MCPEmbedding object
type MCPEmbeddingReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	PlatformDetector *ctrlutil.SharedPlatformDetector
	ImageValidation  validation.ImageValidation
}

const (
	// embeddingContainerName is the name of the embedding container used in pod templates
	embeddingContainerName = "embedding"

	// embeddingFinalizerName is the finalizer name for MCPEmbedding resources
	embeddingFinalizerName = "mcpembedding.toolhive.stacklok.dev/finalizer"

	// modelCacheMountPath is the mount path for the model cache volume
	modelCacheMountPath = "/data"
)

//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpembeddings,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpembeddings/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=mcpembeddings/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *MCPEmbeddingReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the MCPEmbedding instance
	embedding := &mcpv1alpha1.MCPEmbedding{}
	err := r.Get(ctx, req.NamespacedName, embedding)
	if err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("MCPEmbedding resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		ctxLogger.Error(err, "Failed to get MCPEmbedding")
		return ctrl.Result{}, err
	}

	// Perform early validations
	if result, err := r.performValidations(ctx, embedding); err != nil || result.RequeueAfter > 0 {
		return result, err
	}

	// Handle deletion
	if result, done, err := r.handleDeletion(ctx, embedding); done {
		return result, err
	}

	// Add finalizer if needed
	if result, done, err := r.ensureFinalizer(ctx, embedding); done {
		return result, err
	}

	// Ensure PVC for model caching if enabled
	if embedding.IsModelCacheEnabled() {
		if err := r.ensurePVC(ctx, embedding); err != nil {
			ctxLogger.Error(err, "Failed to ensure PVC")
			return ctrl.Result{}, err
		}
	}

	// Ensure deployment exists and is up to date
	if result, done, err := r.ensureDeployment(ctx, embedding); done {
		return result, err
	}

	// Ensure service exists
	if result, done, err := r.ensureService(ctx, embedding); done {
		return result, err
	}

	// Update status with the service URL
	if result, done, err := r.updateServiceURL(ctx, embedding); done {
		return result, err
	}

	// Update the MCPEmbedding status
	if err := r.updateMCPEmbeddingStatus(ctx, embedding); err != nil {
		ctxLogger.Error(err, "Failed to update MCPEmbedding status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// performValidations performs all early validations for the MCPEmbedding
//
//nolint:unparam // error return kept for consistency with reconciler pattern
func (r *MCPEmbeddingReconciler) performValidations(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) (ctrl.Result, error) {
	// Check if the GroupRef is valid if specified
	r.validateGroupRef(ctx, embedding)

	// Validate PodTemplateSpec early
	if !r.validateAndUpdatePodTemplateStatus(ctx, embedding) {
		return ctrl.Result{}, nil
	}

	// Validate image
	if err := r.validateImage(ctx, embedding); err != nil {
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of MCPEmbedding resources
//
//nolint:unparam // ctrl.Result return kept for consistency with reconciler pattern
func (r *MCPEmbeddingReconciler) handleDeletion(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) (ctrl.Result, bool, error) {
	if embedding.GetDeletionTimestamp() == nil {
		return ctrl.Result{}, false, nil
	}

	if controllerutil.ContainsFinalizer(embedding, embeddingFinalizerName) {
		r.finalizeMCPEmbedding(ctx, embedding)

		controllerutil.RemoveFinalizer(embedding, embeddingFinalizerName)
		err := r.Update(ctx, embedding)
		if err != nil {
			return ctrl.Result{}, true, err
		}
	}
	return ctrl.Result{}, true, nil
}

// ensureFinalizer ensures the finalizer is added to the MCPEmbedding
//
//nolint:unparam // ctrl.Result return kept for consistency with reconciler pattern
func (r *MCPEmbeddingReconciler) ensureFinalizer(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) (ctrl.Result, bool, error) {
	if controllerutil.ContainsFinalizer(embedding, embeddingFinalizerName) {
		return ctrl.Result{}, false, nil
	}

	controllerutil.AddFinalizer(embedding, embeddingFinalizerName)
	err := r.Update(ctx, embedding)
	if err != nil {
		return ctrl.Result{}, true, err
	}
	return ctrl.Result{}, false, nil
}

// ensureDeployment ensures the deployment exists and is up to date
func (r *MCPEmbeddingReconciler) ensureDeployment(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) (ctrl.Result, bool, error) {
	ctxLogger := log.FromContext(ctx)

	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: embedding.Name, Namespace: embedding.Namespace}, deployment)
	if err != nil && errors.IsNotFound(err) {
		dep := r.deploymentForEmbedding(ctx, embedding)
		if dep == nil {
			ctxLogger.Error(nil, "Failed to create Deployment object")
			return ctrl.Result{}, true, fmt.Errorf("failed to create Deployment object")
		}
		ctxLogger.Info("Creating a new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
		err = r.Create(ctx, dep)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new Deployment", "Deployment.Namespace", dep.Namespace, "Deployment.Name", dep.Name)
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{Requeue: true}, true, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Deployment")
		return ctrl.Result{}, true, err
	}

	// Ensure the deployment size matches the spec
	desiredReplicas := embedding.GetReplicas()
	if *deployment.Spec.Replicas != desiredReplicas {
		deployment.Spec.Replicas = &desiredReplicas
		err = r.Update(ctx, deployment)
		if err != nil {
			ctxLogger.Error(err, "Failed to update Deployment replicas",
				"Deployment.Namespace", deployment.Namespace,
				"Deployment.Name", deployment.Name)
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{Requeue: true}, true, nil
	}

	// Check if the deployment spec changed
	if r.deploymentNeedsUpdate(ctx, deployment, embedding) {
		newDeployment := r.deploymentForEmbedding(ctx, embedding)
		deployment.Spec = newDeployment.Spec
		err = r.Update(ctx, deployment)
		if err != nil {
			ctxLogger.Error(err, "Failed to update Deployment",
				"Deployment.Namespace", deployment.Namespace,
				"Deployment.Name", deployment.Name)
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{Requeue: true}, true, nil
	}

	return ctrl.Result{}, false, nil
}

// ensureService ensures the service exists
func (r *MCPEmbeddingReconciler) ensureService(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) (ctrl.Result, bool, error) {
	ctxLogger := log.FromContext(ctx)

	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: embedding.Name, Namespace: embedding.Namespace}, service)
	if err != nil && errors.IsNotFound(err) {
		svc := r.serviceForEmbedding(ctx, embedding)
		if svc == nil {
			ctxLogger.Error(nil, "Failed to create Service object")
			return ctrl.Result{}, true, fmt.Errorf("failed to create Service object")
		}
		ctxLogger.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		err = r.Create(ctx, svc)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, true, err
		}
		return ctrl.Result{Requeue: true}, true, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Service")
		return ctrl.Result{}, true, err
	}

	return ctrl.Result{}, false, nil
}

// updateServiceURL updates the status with the service URL
//
//nolint:unparam // ctrl.Result return kept for consistency with reconciler pattern
func (r *MCPEmbeddingReconciler) updateServiceURL(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) (ctrl.Result, bool, error) {
	ctxLogger := log.FromContext(ctx)

	if embedding.Status.URL != "" {
		return ctrl.Result{}, false, nil
	}

	embedding.Status.URL = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
		embedding.Name, embedding.Namespace, embedding.GetPort())
	err := r.Status().Update(ctx, embedding)
	if err != nil {
		ctxLogger.Error(err, "Failed to update MCPEmbedding status")
		return ctrl.Result{}, true, err
	}

	return ctrl.Result{}, false, nil
}

// validateGroupRef validates the GroupRef if specified
func (r *MCPEmbeddingReconciler) validateGroupRef(ctx context.Context, embedding *mcpv1alpha1.MCPEmbedding) {
	if embedding.Spec.GroupRef == "" {
		return
	}

	ctxLogger := log.FromContext(ctx)

	group := &mcpv1alpha1.MCPGroup{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: embedding.Namespace, Name: embedding.Spec.GroupRef}, group); err != nil {
		ctxLogger.Error(err, "Failed to validate GroupRef")
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefNotFound,
			Message:            fmt.Sprintf("MCPGroup '%s' not found in namespace '%s'", embedding.Spec.GroupRef, embedding.Namespace),
			ObservedGeneration: embedding.Generation,
		})
	} else if group.Status.Phase != mcpv1alpha1.MCPGroupPhaseReady {
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefNotReady,
			Message:            fmt.Sprintf("MCPGroup '%s' is not ready (current phase: %s)", embedding.Spec.GroupRef, group.Status.Phase),
			ObservedGeneration: embedding.Generation,
		})
	} else {
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionGroupRefValidated,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonGroupRefValidated,
			Message:            fmt.Sprintf("MCPGroup '%s' is valid and ready", embedding.Spec.GroupRef),
			ObservedGeneration: embedding.Generation,
		})
	}

	if err := r.Status().Update(ctx, embedding); err != nil {
		ctxLogger.Error(err, "Failed to update MCPEmbedding status after GroupRef validation")
	}
}

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and updates the MCPEmbedding status
func (r *MCPEmbeddingReconciler) validateAndUpdatePodTemplateStatus(
	ctx context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) bool {
	ctxLogger := log.FromContext(ctx)

	if embedding.Spec.PodTemplateSpec == nil {
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionPodTemplateValid,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonPodTemplateValid,
			Message:            "No PodTemplateSpec provided",
			ObservedGeneration: embedding.Generation,
		})
		return true
	}

	// Parse and validate PodTemplateSpec using builder
	_, err := ctrlutil.NewPodTemplateSpecBuilder(embedding.Spec.PodTemplateSpec, embeddingContainerName)
	if err != nil {
		ctxLogger.Error(err, "Invalid PodTemplateSpec")
		embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhaseFailed
		embedding.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", err)
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionPodTemplateValid,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonPodTemplateInvalid,
			Message:            fmt.Sprintf("Invalid PodTemplateSpec: %v", err),
			ObservedGeneration: embedding.Generation,
		})
		if statusErr := r.Status().Update(ctx, embedding); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPEmbedding status after PodTemplateSpec validation error")
		}
		r.Recorder.Event(embedding, corev1.EventTypeWarning, "ValidationFailed", fmt.Sprintf("Invalid PodTemplateSpec: %v", err))
		return false
	}

	meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
		Type:               mcpv1alpha1.ConditionPodTemplateValid,
		Status:             metav1.ConditionTrue,
		Reason:             mcpv1alpha1.ConditionReasonPodTemplateValid,
		Message:            "PodTemplateSpec is valid",
		ObservedGeneration: embedding.Generation,
	})

	return true
}

// validateImage validates the embedding image
func (r *MCPEmbeddingReconciler) validateImage(ctx context.Context, embedding *mcpv1alpha1.MCPEmbedding) error {
	ctxLogger := log.FromContext(ctx)

	imageValidator := validation.NewImageValidator(r.Client, embedding.Namespace, r.ImageValidation)
	err := imageValidator.ValidateImage(ctx, embedding.Spec.Image, embedding.ObjectMeta)

	if err == validation.ErrImageNotChecked {
		ctxLogger.Info("Image validation skipped - no enforcement configured")
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionImageValidated,
			Status:  metav1.ConditionTrue,
			Reason:  mcpv1alpha1.ConditionReasonImageValidationSkipped,
			Message: "Image validation was not performed (no enforcement configured)",
		})
		if statusErr := r.Status().Update(ctx, embedding); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPEmbedding status after image validation")
		}
		return nil
	} else if err == validation.ErrImageInvalid {
		ctxLogger.Error(err, "MCPEmbedding image validation failed", "image", embedding.Spec.Image)
		embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhaseFailed
		embedding.Status.Message = err.Error()
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionImageValidated,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonImageValidationFailed,
			Message: err.Error(),
		})
		if statusErr := r.Status().Update(ctx, embedding); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPEmbedding status after validation error")
		}
		return err
	} else if err != nil {
		ctxLogger.Error(err, "MCPEmbedding image validation system error", "image", embedding.Spec.Image)
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionImageValidated,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonImageValidationError,
			Message: fmt.Sprintf("Error checking image validity: %v", err),
		})
		if statusErr := r.Status().Update(ctx, embedding); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update MCPEmbedding status after validation error")
		}
		return err
	}

	ctxLogger.Info("Image validation passed", "image", embedding.Spec.Image)
	meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionImageValidated,
		Status:  metav1.ConditionTrue,
		Reason:  mcpv1alpha1.ConditionReasonImageValidationSuccess,
		Message: "Image validation passed",
	})
	if statusErr := r.Status().Update(ctx, embedding); statusErr != nil {
		ctxLogger.Error(statusErr, "Failed to update MCPEmbedding status after image validation")
	}

	return nil
}

// ensurePVC ensures the PVC for model caching exists
func (r *MCPEmbeddingReconciler) ensurePVC(ctx context.Context, embedding *mcpv1alpha1.MCPEmbedding) error {
	ctxLogger := log.FromContext(ctx)

	pvcName := fmt.Sprintf("%s-model-cache", embedding.Name)
	pvc := &corev1.PersistentVolumeClaim{}

	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: embedding.Namespace}, pvc)
	if err != nil && errors.IsNotFound(err) {
		pvc = r.pvcForEmbedding(embedding)
		ctxLogger.Info("Creating a new PVC", "PVC.Namespace", pvc.Namespace, "PVC.Name", pvc.Name)

		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionVolumeReady,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonVolumeCreating,
			Message:            "Creating PersistentVolumeClaim for model cache",
			ObservedGeneration: embedding.Generation,
		})

		err = r.Create(ctx, pvc)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new PVC", "PVC.Namespace", pvc.Namespace, "PVC.Name", pvc.Name)
			meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
				Type:               mcpv1alpha1.ConditionVolumeReady,
				Status:             metav1.ConditionFalse,
				Reason:             mcpv1alpha1.ConditionReasonVolumeFailed,
				Message:            fmt.Sprintf("Failed to create PVC: %v", err),
				ObservedGeneration: embedding.Generation,
			})
			return err
		}

		r.Recorder.Event(embedding, corev1.EventTypeNormal, "PVCCreated", fmt.Sprintf("Created PVC %s for model caching", pvcName))
		return nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get PVC")
		return err
	}

	// PVC exists, check if it's bound
	if pvc.Status.Phase == corev1.ClaimBound {
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionVolumeReady,
			Status:             metav1.ConditionTrue,
			Reason:             mcpv1alpha1.ConditionReasonVolumeReady,
			Message:            "PersistentVolumeClaim is bound and ready",
			ObservedGeneration: embedding.Generation,
		})
	} else {
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionVolumeReady,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonVolumeCreating,
			Message:            fmt.Sprintf("PersistentVolumeClaim is in phase: %s", pvc.Status.Phase),
			ObservedGeneration: embedding.Generation,
		})
	}

	return nil
}

// pvcForEmbedding creates a PVC for the embedding model cache
func (r *MCPEmbeddingReconciler) pvcForEmbedding(embedding *mcpv1alpha1.MCPEmbedding) *corev1.PersistentVolumeClaim {
	pvcName := fmt.Sprintf("%s-model-cache", embedding.Name)

	size := "10Gi"
	if embedding.Spec.ModelCache.Size != "" {
		size = embedding.Spec.ModelCache.Size
	}

	accessMode := corev1.ReadWriteOnce
	if embedding.Spec.ModelCache.AccessMode != "" {
		accessMode = corev1.PersistentVolumeAccessMode(embedding.Spec.ModelCache.AccessMode)
	}

	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: embedding.Namespace,
			Labels:    r.labelsForEmbedding(embedding),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{accessMode},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}

	if embedding.Spec.ModelCache.StorageClassName != nil {
		pvc.Spec.StorageClassName = embedding.Spec.ModelCache.StorageClassName
	}

	// Apply resource overrides if specified
	if embedding.Spec.ResourceOverrides != nil && embedding.Spec.ResourceOverrides.PersistentVolumeClaim != nil {
		if embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Annotations != nil {
			pvc.Annotations = embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Annotations
		}
		if embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Labels != nil {
			maps.Copy(pvc.Labels, embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Labels)
		}
	}

	if err := ctrl.SetControllerReference(embedding, pvc, r.Scheme); err != nil {
		return nil
	}
	return pvc
}

// deploymentForEmbedding creates a Deployment for the embedding server
func (r *MCPEmbeddingReconciler) deploymentForEmbedding(
	_ context.Context,
	embedding *mcpv1alpha1.MCPEmbedding,
) *appsv1.Deployment {
	replicas := embedding.GetReplicas()
	labels := r.labelsForEmbedding(embedding)

	// Build container
	container := r.buildEmbeddingContainer(embedding)

	// Build pod template
	podTemplate := r.buildPodTemplate(embedding, labels, container)

	// Apply deployment overrides
	annotations := r.applyDeploymentOverrides(embedding, &podTemplate)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        embedding.Name,
			Namespace:   embedding.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: podTemplate,
		},
	}

	if err := ctrl.SetControllerReference(embedding, deployment, r.Scheme); err != nil {
		return nil
	}
	return deployment
}

// buildEmbeddingContainer builds the container spec for the embedding server
func (r *MCPEmbeddingReconciler) buildEmbeddingContainer(embedding *mcpv1alpha1.MCPEmbedding) corev1.Container {
	// Build container args
	args := []string{
		"--model-id", embedding.Spec.Model,
		"--port", fmt.Sprintf("%d", embedding.GetPort()),
	}
	args = append(args, embedding.Spec.Args...)

	// Build environment variables
	envVars := r.buildEnvVars(embedding)

	// Build container
	container := corev1.Container{
		Name:            embeddingContainerName,
		Image:           embedding.Spec.Image,
		Args:            args,
		Env:             envVars,
		ImagePullPolicy: corev1.PullPolicy(embedding.GetImagePullPolicy()),
		Ports: []corev1.ContainerPort{
			{
				Name:          "http",
				ContainerPort: embedding.GetPort(),
				Protocol:      corev1.ProtocolTCP,
			},
		},
		LivenessProbe:  r.buildLivenessProbe(embedding),
		ReadinessProbe: r.buildReadinessProbe(embedding),
	}

	// Add volume mount and HF_HOME for model cache if enabled
	if embedding.IsModelCacheEnabled() {
		container.VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "model-cache",
				MountPath: modelCacheMountPath,
			},
		}
		container.Env = append(container.Env, corev1.EnvVar{
			Name:  "HF_HOME",
			Value: modelCacheMountPath,
		})
	}

	// Add resources if specified
	r.applyResourceRequirements(embedding, &container)

	return container
}

// buildEnvVars builds environment variables for the container
func (*MCPEmbeddingReconciler) buildEnvVars(embedding *mcpv1alpha1.MCPEmbedding) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:  "MODEL_ID",
			Value: embedding.Spec.Model,
		},
	}
	for _, env := range embedding.Spec.Env {
		envVars = append(envVars, corev1.EnvVar{
			Name:  env.Name,
			Value: env.Value,
		})
	}
	return envVars
}

// buildLivenessProbe builds the liveness probe for the container
func (*MCPEmbeddingReconciler) buildLivenessProbe(embedding *mcpv1alpha1.MCPEmbedding) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr.FromInt(int(embedding.GetPort())),
			},
		},
		InitialDelaySeconds: 60,
		PeriodSeconds:       30,
		TimeoutSeconds:      10,
		FailureThreshold:    3,
	}
}

// buildReadinessProbe builds the readiness probe for the container
func (*MCPEmbeddingReconciler) buildReadinessProbe(embedding *mcpv1alpha1.MCPEmbedding) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path: "/health",
				Port: intstr.FromInt(int(embedding.GetPort())),
			},
		},
		InitialDelaySeconds: 30,
		PeriodSeconds:       10,
		TimeoutSeconds:      5,
		FailureThreshold:    3,
	}
}

// applyResourceRequirements applies resource requirements to the container
func (*MCPEmbeddingReconciler) applyResourceRequirements(embedding *mcpv1alpha1.MCPEmbedding, container *corev1.Container) {
	if embedding.Spec.Resources.Limits.CPU == "" && embedding.Spec.Resources.Limits.Memory == "" &&
		embedding.Spec.Resources.Requests.CPU == "" && embedding.Spec.Resources.Requests.Memory == "" {
		return
	}

	container.Resources = corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}

	if embedding.Spec.Resources.Limits.CPU != "" {
		container.Resources.Limits[corev1.ResourceCPU] = resource.MustParse(embedding.Spec.Resources.Limits.CPU)
	}
	if embedding.Spec.Resources.Limits.Memory != "" {
		container.Resources.Limits[corev1.ResourceMemory] = resource.MustParse(embedding.Spec.Resources.Limits.Memory)
	}
	if embedding.Spec.Resources.Requests.CPU != "" {
		container.Resources.Requests[corev1.ResourceCPU] = resource.MustParse(embedding.Spec.Resources.Requests.CPU)
	}
	if embedding.Spec.Resources.Requests.Memory != "" {
		container.Resources.Requests[corev1.ResourceMemory] = resource.MustParse(embedding.Spec.Resources.Requests.Memory)
	}
}

// buildPodTemplate builds the pod template for the deployment
func (r *MCPEmbeddingReconciler) buildPodTemplate(
	embedding *mcpv1alpha1.MCPEmbedding,
	labels map[string]string,
	container corev1.Container,
) corev1.PodTemplateSpec {
	podTemplate := corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{container},
		},
	}

	// Add volume for model cache if enabled
	if embedding.IsModelCacheEnabled() {
		pvcName := fmt.Sprintf("%s-model-cache", embedding.Name)
		podTemplate.Spec.Volumes = []corev1.Volume{
			{
				Name: "model-cache",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			},
		}
	}

	// Merge with user-provided PodTemplateSpec if specified
	r.mergePodTemplateSpec(embedding, &podTemplate)

	return podTemplate
}

// mergePodTemplateSpec merges user-provided PodTemplateSpec customizations
func (r *MCPEmbeddingReconciler) mergePodTemplateSpec(embedding *mcpv1alpha1.MCPEmbedding, podTemplate *corev1.PodTemplateSpec) {
	if embedding.Spec.PodTemplateSpec == nil {
		return
	}

	builder, err := ctrlutil.NewPodTemplateSpecBuilder(embedding.Spec.PodTemplateSpec, embeddingContainerName)
	if err != nil {
		return
	}

	userTemplate := builder.Build()
	if userTemplate == nil {
		return
	}

	// Merge user customizations into base pod template
	if userTemplate.Spec.NodeSelector != nil {
		podTemplate.Spec.NodeSelector = userTemplate.Spec.NodeSelector
	}
	if userTemplate.Spec.Affinity != nil {
		podTemplate.Spec.Affinity = userTemplate.Spec.Affinity
	}
	if len(userTemplate.Spec.Tolerations) > 0 {
		podTemplate.Spec.Tolerations = userTemplate.Spec.Tolerations
	}
	if userTemplate.Spec.SecurityContext != nil {
		podTemplate.Spec.SecurityContext = userTemplate.Spec.SecurityContext
	}

	// Merge container-level customizations
	r.mergeContainerSecurityContext(podTemplate, userTemplate)
}

// mergeContainerSecurityContext merges container-level security context
func (*MCPEmbeddingReconciler) mergeContainerSecurityContext(
	podTemplate *corev1.PodTemplateSpec,
	userTemplate *corev1.PodTemplateSpec,
) {
	for i := range podTemplate.Spec.Containers {
		if podTemplate.Spec.Containers[i].Name != embeddingContainerName {
			continue
		}
		for _, userContainer := range userTemplate.Spec.Containers {
			if userContainer.Name == embeddingContainerName && userContainer.SecurityContext != nil {
				podTemplate.Spec.Containers[i].SecurityContext = userContainer.SecurityContext
				break
			}
		}
		break
	}
}

// applyDeploymentOverrides applies deployment-level overrides and returns annotations
func (*MCPEmbeddingReconciler) applyDeploymentOverrides(
	embedding *mcpv1alpha1.MCPEmbedding,
	podTemplate *corev1.PodTemplateSpec,
) map[string]string {
	annotations := make(map[string]string)

	if embedding.Spec.ResourceOverrides == nil || embedding.Spec.ResourceOverrides.Deployment == nil {
		return annotations
	}

	if embedding.Spec.ResourceOverrides.Deployment.Annotations != nil {
		maps.Copy(annotations, embedding.Spec.ResourceOverrides.Deployment.Annotations)
	}

	if embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides != nil {
		if podTemplate.Annotations == nil {
			podTemplate.Annotations = make(map[string]string)
		}
		if embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Annotations != nil {
			maps.Copy(
				podTemplate.Annotations,
				embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Annotations,
			)
		}
		if embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Labels != nil {
			maps.Copy(podTemplate.Labels, embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Labels)
		}
	}

	return annotations
}

// serviceForEmbedding creates a Service for the embedding server
func (r *MCPEmbeddingReconciler) serviceForEmbedding(_ context.Context, embedding *mcpv1alpha1.MCPEmbedding) *corev1.Service {
	labels := r.labelsForEmbedding(embedding)
	annotations := make(map[string]string)

	// Apply service overrides if specified
	if embedding.Spec.ResourceOverrides != nil && embedding.Spec.ResourceOverrides.Service != nil {
		if embedding.Spec.ResourceOverrides.Service.Annotations != nil {
			maps.Copy(annotations, embedding.Spec.ResourceOverrides.Service.Annotations)
		}
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        embedding.Name,
			Namespace:   embedding.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{
					Name:       "http",
					Port:       embedding.GetPort(),
					TargetPort: intstr.FromInt(int(embedding.GetPort())),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}

	if err := ctrl.SetControllerReference(embedding, service, r.Scheme); err != nil {
		return nil
	}
	return service
}

// labelsForEmbedding returns the labels for the embedding resources
func (*MCPEmbeddingReconciler) labelsForEmbedding(embedding *mcpv1alpha1.MCPEmbedding) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/name":       "mcpembedding",
		"app.kubernetes.io/instance":   embedding.Name,
		"app.kubernetes.io/component":  "embedding-server",
		"app.kubernetes.io/managed-by": "toolhive-operator",
	}

	if embedding.Spec.GroupRef != "" {
		labels["toolhive.stacklok.dev/group"] = embedding.Spec.GroupRef
	}

	return labels
}

// deploymentNeedsUpdate checks if the deployment needs to be updated
func (r *MCPEmbeddingReconciler) deploymentNeedsUpdate(
	ctx context.Context,
	deployment *appsv1.Deployment,
	embedding *mcpv1alpha1.MCPEmbedding,
) bool {
	newDeployment := r.deploymentForEmbedding(ctx, embedding)

	// Compare important fields
	if !reflect.DeepEqual(deployment.Spec.Template.Spec.Containers, newDeployment.Spec.Template.Spec.Containers) {
		return true
	}

	if !reflect.DeepEqual(deployment.Spec.Template.Spec.Volumes, newDeployment.Spec.Template.Spec.Volumes) {
		return true
	}

	return false
}

// updateMCPEmbeddingStatus updates the status based on deployment state
func (r *MCPEmbeddingReconciler) updateMCPEmbeddingStatus(ctx context.Context, embedding *mcpv1alpha1.MCPEmbedding) error {
	ctxLogger := log.FromContext(ctx)

	deployment := &appsv1.Deployment{}
	err := r.Get(ctx, types.NamespacedName{Name: embedding.Name, Namespace: embedding.Namespace}, deployment)
	if err != nil {
		if errors.IsNotFound(err) {
			embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhasePending
			embedding.Status.ReadyReplicas = 0
		} else {
			return err
		}
	} else {
		embedding.Status.ReadyReplicas = deployment.Status.ReadyReplicas
		embedding.Status.ObservedGeneration = embedding.Generation

		// Determine phase based on deployment status
		if deployment.Status.ReadyReplicas > 0 {
			embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhaseRunning
			embedding.Status.Message = "Embedding server is running"
		} else if deployment.Status.Replicas > 0 && deployment.Status.ReadyReplicas == 0 {
			// Check if pods are downloading the model
			embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhaseDownloading
			embedding.Status.Message = "Downloading embedding model"
		} else {
			embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhasePending
			embedding.Status.Message = "Waiting for deployment"
		}
	}

	err = r.Status().Update(ctx, embedding)
	if err != nil {
		ctxLogger.Error(err, "Failed to update MCPEmbedding status")
		return err
	}

	return nil
}

// finalizeMCPEmbedding performs cleanup before the MCPEmbedding is deleted
func (r *MCPEmbeddingReconciler) finalizeMCPEmbedding(ctx context.Context, embedding *mcpv1alpha1.MCPEmbedding) {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Info("Finalizing MCPEmbedding", "name", embedding.Name)

	// Update status to Terminating
	embedding.Status.Phase = mcpv1alpha1.MCPEmbeddingPhaseTerminating
	if err := r.Status().Update(ctx, embedding); err != nil {
		ctxLogger.Error(err, "Failed to update MCPEmbedding status to Terminating")
	}

	// Cleanup logic here if needed
	// For now, Kubernetes will handle cascade deletion of owned resources

	r.Recorder.Event(embedding, corev1.EventTypeNormal, "Deleted", "MCPEmbedding has been finalized")
}

// SetupWithManager sets up the controller with the Manager.
func (r *MCPEmbeddingReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPEmbedding{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
