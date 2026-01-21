// Package controllers contains the reconciliation logic for the EmbeddingServer custom resource.
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

// EmbeddingServerReconciler reconciles a EmbeddingServer object
type EmbeddingServerReconciler struct {
	client.Client
	Scheme           *runtime.Scheme
	Recorder         record.EventRecorder
	PlatformDetector *ctrlutil.SharedPlatformDetector
	ImageValidation  validation.ImageValidation
}

const (
	// embeddingContainerName is the name of the embedding container used in pod templates
	embeddingContainerName = "embedding"

	// embeddingFinalizerName is the finalizer name for EmbeddingServer resources
	embeddingFinalizerName = "embeddingserver.toolhive.stacklok.dev/finalizer"

	// modelCacheMountPath is the mount path for the model cache volume
	modelCacheMountPath = "/data"
)

//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=embeddingservers,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=embeddingservers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=toolhive.stacklok.dev,resources=embeddingservers/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
//nolint:gocyclo // Reconciliation logic complexity is acceptable
func (r *EmbeddingServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Fetch the EmbeddingServer instance
	embedding := &mcpv1alpha1.EmbeddingServer{}
	err := r.Get(ctx, req.NamespacedName, embedding)
	if err != nil {
		if errors.IsNotFound(err) {
			ctxLogger.Info("EmbeddingServer resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		ctxLogger.Error(err, "Failed to get EmbeddingServer")
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

	// Track if we need to requeue after status update
	var requeueResult ctrl.Result

	// Ensure statefulset exists and is up to date
	if result, err := r.ensureStatefulSet(ctx, embedding); err != nil {
		return ctrl.Result{}, err
	} else if result.RequeueAfter > 0 {
		requeueResult = result
	}

	// Ensure service exists
	if result, err := r.ensureService(ctx, embedding); err != nil {
		return ctrl.Result{}, err
	} else if result.RequeueAfter > 0 {
		// If we already have a requeue scheduled, keep the shorter duration
		if requeueResult.RequeueAfter == 0 || (result.RequeueAfter > 0 && result.RequeueAfter < requeueResult.RequeueAfter) {
			requeueResult = result
		}
	}

	// Always update the EmbeddingServer status before returning
	if err := r.updateEmbeddingServerStatus(ctx, embedding); err != nil {
		ctxLogger.Error(err, "Failed to update EmbeddingServer status")
		return ctrl.Result{}, err
	}

	return requeueResult, nil
}

// performValidations performs all early validations for the EmbeddingServer
//
//nolint:unparam // error return kept for consistency with reconciler pattern
func (r *EmbeddingServerReconciler) performValidations(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	// Validate PodTemplateSpec early
	if !r.validateAndUpdatePodTemplateStatus(ctx, embedding) {
		// Status fields were set by validateAndUpdatePodTemplateStatus, now update
		if err := r.Status().Update(ctx, embedding); err != nil {
			ctxLogger.Error(err, "Failed to update EmbeddingServer status after PodTemplateSpec validation failure")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Validate image
	if err := r.validateImage(ctx, embedding); err != nil {
		// Status fields were set by validateImage, now update
		if statusErr := r.Status().Update(ctx, embedding); statusErr != nil {
			ctxLogger.Error(statusErr, "Failed to update EmbeddingServer status after image validation failure")
			return ctrl.Result{}, statusErr
		}
		// We requeue to retry validation after image issues are resolved
		ctxLogger.Error(err, "Image validation failed, will retry",
			"image", embedding.Spec.Image,
			"requeueAfter", 5*time.Minute)
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	return ctrl.Result{}, nil
}

// handleDeletion handles the deletion of EmbeddingServer resources
//
//nolint:unparam // ctrl.Result return kept for consistency with reconciler pattern
func (r *EmbeddingServerReconciler) handleDeletion(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) (ctrl.Result, bool, error) {
	if embedding.GetDeletionTimestamp() == nil {
		return ctrl.Result{}, false, nil
	}

	if controllerutil.ContainsFinalizer(embedding, embeddingFinalizerName) {
		r.finalizeEmbeddingServer(ctx, embedding)

		controllerutil.RemoveFinalizer(embedding, embeddingFinalizerName)
		err := r.Update(ctx, embedding)
		if err != nil {
			return ctrl.Result{}, true, err
		}
	}
	return ctrl.Result{}, true, nil
}

// ensureFinalizer ensures the finalizer is added to the EmbeddingServer
//
//nolint:unparam // ctrl.Result return kept for consistency with reconciler pattern
func (r *EmbeddingServerReconciler) ensureFinalizer(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
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

// ensureStatefulSet ensures the statefulset exists and is up to date
func (r *EmbeddingServerReconciler) ensureStatefulSet(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	statefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: embedding.Name, Namespace: embedding.Namespace}, statefulSet)
	if err != nil && errors.IsNotFound(err) {
		sts := r.statefulSetForEmbedding(ctx, embedding)
		if sts == nil {
			ctxLogger.Error(nil, "Failed to create StatefulSet object")
			return ctrl.Result{}, fmt.Errorf("failed to create StatefulSet object")
		}
		ctxLogger.Info("Creating a new StatefulSet", "StatefulSet.Namespace", sts.Namespace, "StatefulSet.Name", sts.Name)
		err = r.Create(ctx, sts)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new StatefulSet", "StatefulSet.Namespace", sts.Namespace, "StatefulSet.Name", sts.Name)
			return ctrl.Result{}, err
		}
		// StatefulSet created successfully, continue to ensure service
		return ctrl.Result{}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get StatefulSet")
		return ctrl.Result{}, err
	}

	// Ensure the statefulset size matches the spec
	desiredReplicas := embedding.GetReplicas()
	if *statefulSet.Spec.Replicas != desiredReplicas {
		statefulSet.Spec.Replicas = &desiredReplicas
		if err := r.updateStatefulSetWithRetry(ctx, statefulSet); err != nil {
			ctxLogger.Error(err, "Failed to update StatefulSet replicas",
				"StatefulSet.Namespace", statefulSet.Namespace,
				"StatefulSet.Name", statefulSet.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	// Check if the statefulset spec changed
	if r.statefulSetNeedsUpdate(ctx, statefulSet, embedding) {
		newStatefulSet := r.statefulSetForEmbedding(ctx, embedding)
		statefulSet.Spec = newStatefulSet.Spec
		statefulSet.Annotations = newStatefulSet.Annotations
		statefulSet.Labels = newStatefulSet.Labels
		if err := r.updateStatefulSetWithRetry(ctx, statefulSet); err != nil {
			ctxLogger.Error(err, "Failed to update StatefulSet",
				"StatefulSet.Namespace", statefulSet.Namespace,
				"StatefulSet.Name", statefulSet.Name)
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// updateStatefulSetWithRetry updates the statefulset
// The reconciler loop will automatically retry on conflicts
func (r *EmbeddingServerReconciler) updateStatefulSetWithRetry(
	ctx context.Context,
	statefulSet *appsv1.StatefulSet,
) error {
	return r.Update(ctx, statefulSet)
}

// ensureService ensures the service exists and is up to date
//
//nolint:unparam // ctrl.Result return kept for consistency with reconciler pattern
func (r *EmbeddingServerReconciler) ensureService(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) (ctrl.Result, error) {
	ctxLogger := log.FromContext(ctx)

	service := &corev1.Service{}
	err := r.Get(ctx, types.NamespacedName{Name: embedding.Name, Namespace: embedding.Namespace}, service)
	if err != nil && errors.IsNotFound(err) {
		svc := r.serviceForEmbedding(ctx, embedding)
		if svc == nil {
			ctxLogger.Error(nil, "Failed to create Service object")
			return ctrl.Result{}, fmt.Errorf("failed to create Service object")
		}
		ctxLogger.Info("Creating a new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
		err = r.Create(ctx, svc)
		if err != nil {
			ctxLogger.Error(err, "Failed to create new Service", "Service.Namespace", svc.Namespace, "Service.Name", svc.Name)
			return ctrl.Result{}, err
		}
		// Service created successfully, continue to update status
		return ctrl.Result{}, nil
	} else if err != nil {
		ctxLogger.Error(err, "Failed to get Service")
		return ctrl.Result{}, err
	}

	// Check if the service needs to be updated
	if r.serviceNeedsUpdate(service, embedding) {
		desiredService := r.serviceForEmbedding(ctx, embedding)
		service.Spec.Ports = desiredService.Spec.Ports
		service.Labels = desiredService.Labels
		service.Annotations = desiredService.Annotations
		// Preserve ClusterIP as it's immutable
		if err := r.Update(ctx, service); err != nil {
			ctxLogger.Error(err, "Failed to update Service",
				"Service.Namespace", service.Namespace,
				"Service.Name", service.Name)
			return ctrl.Result{}, err
		}
		ctxLogger.Info("Updated Service", "Service.Namespace", service.Namespace, "Service.Name", service.Name)
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}

	return ctrl.Result{}, nil
}

// serviceNeedsUpdate checks if the service needs to be updated based on the embedding spec
func (*EmbeddingServerReconciler) serviceNeedsUpdate(
	service *corev1.Service,
	embedding *mcpv1alpha1.EmbeddingServer,
) bool {
	desiredPort := embedding.GetPort()

	// Check if any port has changed
	for _, port := range service.Spec.Ports {
		if port.Name == "http" && port.Port != desiredPort {
			return true
		}
	}

	// Check ResourceOverrides (annotations and labels)
	expectedAnnotations := make(map[string]string)
	expectedLabels := make(map[string]string)

	if embedding.Spec.ResourceOverrides != nil && embedding.Spec.ResourceOverrides.Service != nil {
		if embedding.Spec.ResourceOverrides.Service.Annotations != nil {
			maps.Copy(expectedAnnotations, embedding.Spec.ResourceOverrides.Service.Annotations)
		}
		if embedding.Spec.ResourceOverrides.Service.Labels != nil {
			maps.Copy(expectedLabels, embedding.Spec.ResourceOverrides.Service.Labels)
		}
	}

	// Check if expected annotations are present in service
	for key, value := range expectedAnnotations {
		if service.Annotations[key] != value {
			return true
		}
	}

	// Check if expected labels are present in service
	for key, value := range expectedLabels {
		if service.Labels[key] != value {
			return true
		}
	}

	return false
}

// validateAndUpdatePodTemplateStatus validates the PodTemplateSpec and sets the status condition
// Status is not updated here - it will be updated at the end of reconciliation
func (r *EmbeddingServerReconciler) validateAndUpdatePodTemplateStatus(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
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
		embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhaseFailed
		embedding.Status.Message = fmt.Sprintf("Invalid PodTemplateSpec: %v", err)
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:               mcpv1alpha1.ConditionPodTemplateValid,
			Status:             metav1.ConditionFalse,
			Reason:             mcpv1alpha1.ConditionReasonPodTemplateInvalid,
			Message:            fmt.Sprintf("Invalid PodTemplateSpec: %v", err),
			ObservedGeneration: embedding.Generation,
		})
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

// validateImage validates the embedding image and sets the status condition
// Status is not updated here - it will be updated at the end of reconciliation
func (r *EmbeddingServerReconciler) validateImage(ctx context.Context, embedding *mcpv1alpha1.EmbeddingServer) error {
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
		return nil
	} else if err == validation.ErrImageInvalid {
		ctxLogger.Error(err, "EmbeddingServer image validation failed", "image", embedding.Spec.Image)
		embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhaseFailed
		embedding.Status.Message = err.Error()
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionImageValidated,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonImageValidationFailed,
			Message: err.Error(),
		})
		return err
	} else if err != nil {
		ctxLogger.Error(err, "EmbeddingServer image validation system error", "image", embedding.Spec.Image)
		meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
			Type:    mcpv1alpha1.ConditionImageValidated,
			Status:  metav1.ConditionFalse,
			Reason:  mcpv1alpha1.ConditionReasonImageValidationError,
			Message: fmt.Sprintf("Error checking image validity: %v", err),
		})
		return err
	}

	ctxLogger.Info("Image validation passed", "image", embedding.Spec.Image)
	meta.SetStatusCondition(&embedding.Status.Conditions, metav1.Condition{
		Type:    mcpv1alpha1.ConditionImageValidated,
		Status:  metav1.ConditionTrue,
		Reason:  mcpv1alpha1.ConditionReasonImageValidationSuccess,
		Message: "Image validation passed",
	})

	return nil
}

// statefulSetForEmbedding creates a StatefulSet for the embedding server
func (r *EmbeddingServerReconciler) statefulSetForEmbedding(
	_ context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) *appsv1.StatefulSet {
	replicas := embedding.GetReplicas()
	labels := r.labelsForEmbedding(embedding)

	// Build container
	container := r.buildEmbeddingContainer(embedding)

	// Build pod template
	podTemplate := r.buildPodTemplate(embedding, labels, container)

	// Apply deployment overrides (reuse for StatefulSet pod template)
	stsAnnotations, stsLabels := r.applyDeploymentOverrides(embedding, &podTemplate)

	// Merge ResourceOverrides labels into base labels
	finalLabels := make(map[string]string)
	maps.Copy(finalLabels, labels)
	maps.Copy(finalLabels, stsLabels)

	statefulSet := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        embedding.Name,
			Namespace:   embedding.Namespace,
			Labels:      finalLabels,
			Annotations: stsAnnotations,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas:    &replicas,
			ServiceName: embedding.Name, // Required for StatefulSet
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: podTemplate,
		},
	}

	// Add volumeClaimTemplates if model caching is enabled
	if embedding.IsModelCacheEnabled() {
		statefulSet.Spec.VolumeClaimTemplates = r.buildVolumeClaimTemplates(embedding)
	}

	if err := ctrl.SetControllerReference(embedding, statefulSet, r.Scheme); err != nil {
		return nil
	}
	return statefulSet
}

// buildVolumeClaimTemplates builds the volumeClaimTemplates for the StatefulSet
func (r *EmbeddingServerReconciler) buildVolumeClaimTemplates(
	embedding *mcpv1alpha1.EmbeddingServer,
) []corev1.PersistentVolumeClaim {
	size := "10Gi"
	if embedding.Spec.ModelCache.Size != "" {
		size = embedding.Spec.ModelCache.Size
	}

	accessMode := corev1.ReadWriteOnce
	if embedding.Spec.ModelCache.AccessMode != "" {
		accessMode = corev1.PersistentVolumeAccessMode(embedding.Spec.ModelCache.AccessMode)
	}

	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "model-cache",
			Labels: r.labelsForEmbedding(embedding),
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
		if pvc.Annotations == nil && embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Annotations != nil {
			pvc.Annotations = make(map[string]string)
		}
		if embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Annotations != nil {
			maps.Copy(pvc.Annotations, embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Annotations)
		}
		if embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Labels != nil {
			maps.Copy(pvc.Labels, embedding.Spec.ResourceOverrides.PersistentVolumeClaim.Labels)
		}
	}

	return []corev1.PersistentVolumeClaim{pvc}
}

// buildEmbeddingContainer builds the container spec for the embedding server
func (r *EmbeddingServerReconciler) buildEmbeddingContainer(embedding *mcpv1alpha1.EmbeddingServer) corev1.Container {
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
func (*EmbeddingServerReconciler) buildEnvVars(embedding *mcpv1alpha1.EmbeddingServer) []corev1.EnvVar {
	envVars := []corev1.EnvVar{
		{
			Name:  "MODEL_ID",
			Value: embedding.Spec.Model,
		},
	}

	// Add HuggingFace token from secret if provided
	if embedding.Spec.HFTokenSecretRef != nil {
		envVars = append(envVars, corev1.EnvVar{
			Name: "HF_TOKEN",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: embedding.Spec.HFTokenSecretRef.Name,
					},
					Key: embedding.Spec.HFTokenSecretRef.Key,
				},
			},
		})
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
func (*EmbeddingServerReconciler) buildLivenessProbe(embedding *mcpv1alpha1.EmbeddingServer) *corev1.Probe {
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
func (*EmbeddingServerReconciler) buildReadinessProbe(embedding *mcpv1alpha1.EmbeddingServer) *corev1.Probe {
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
func (*EmbeddingServerReconciler) applyResourceRequirements(embedding *mcpv1alpha1.EmbeddingServer, container *corev1.Container) {
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

// buildPodTemplate builds the pod template for the statefulset
func (r *EmbeddingServerReconciler) buildPodTemplate(
	embedding *mcpv1alpha1.EmbeddingServer,
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

	// Note: Volumes for model cache are managed by StatefulSet volumeClaimTemplates
	// and will be automatically mounted with the name "model-cache"

	// Merge with user-provided PodTemplateSpec if specified
	r.mergePodTemplateSpec(embedding, &podTemplate)

	return podTemplate
}

// mergePodTemplateSpec merges user-provided PodTemplateSpec customizations
func (r *EmbeddingServerReconciler) mergePodTemplateSpec(
	embedding *mcpv1alpha1.EmbeddingServer,
	podTemplate *corev1.PodTemplateSpec,
) {
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
	if userTemplate.Spec.ServiceAccountName != "" {
		podTemplate.Spec.ServiceAccountName = userTemplate.Spec.ServiceAccountName
	}

	// Merge container-level customizations
	r.mergeContainerSecurityContext(podTemplate, userTemplate)
}

// mergeContainerSecurityContext merges container-level security context
func (*EmbeddingServerReconciler) mergeContainerSecurityContext(
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

// applyDeploymentOverrides applies deployment-level overrides and returns annotations and labels
func (*EmbeddingServerReconciler) applyDeploymentOverrides(
	embedding *mcpv1alpha1.EmbeddingServer,
	podTemplate *corev1.PodTemplateSpec,
) (map[string]string, map[string]string) {
	annotations := make(map[string]string)
	labels := make(map[string]string)

	if embedding.Spec.ResourceOverrides == nil || embedding.Spec.ResourceOverrides.Deployment == nil {
		return annotations, labels
	}

	if embedding.Spec.ResourceOverrides.Deployment.Annotations != nil {
		maps.Copy(annotations, embedding.Spec.ResourceOverrides.Deployment.Annotations)
	}

	if embedding.Spec.ResourceOverrides.Deployment.Labels != nil {
		maps.Copy(labels, embedding.Spec.ResourceOverrides.Deployment.Labels)
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

	return annotations, labels
}

// serviceForEmbedding creates a Service for the embedding server
func (r *EmbeddingServerReconciler) serviceForEmbedding(
	_ context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) *corev1.Service {
	labels := r.labelsForEmbedding(embedding)
	annotations := make(map[string]string)

	// Apply service overrides if specified
	finalLabels := make(map[string]string)
	maps.Copy(finalLabels, labels)

	if embedding.Spec.ResourceOverrides != nil && embedding.Spec.ResourceOverrides.Service != nil {
		if embedding.Spec.ResourceOverrides.Service.Annotations != nil {
			maps.Copy(annotations, embedding.Spec.ResourceOverrides.Service.Annotations)
		}
		if embedding.Spec.ResourceOverrides.Service.Labels != nil {
			maps.Copy(finalLabels, embedding.Spec.ResourceOverrides.Service.Labels)
		}
	}

	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        embedding.Name,
			Namespace:   embedding.Namespace,
			Labels:      finalLabels,
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
func (*EmbeddingServerReconciler) labelsForEmbedding(embedding *mcpv1alpha1.EmbeddingServer) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       "embeddingserver",
		"app.kubernetes.io/instance":   embedding.Name,
		"app.kubernetes.io/component":  "embedding-server",
		"app.kubernetes.io/managed-by": "toolhive-operator",
	}
}

// statefulSetNeedsUpdate checks if the statefulset needs to be updated
//
//nolint:gocyclo // Complexity unavoidable due to many field comparisons
func (r *EmbeddingServerReconciler) statefulSetNeedsUpdate(
	_ context.Context,
	statefulSet *appsv1.StatefulSet,
	embedding *mcpv1alpha1.EmbeddingServer,
) bool {
	// Check if the number of replicas changed
	desiredReplicas := embedding.GetReplicas()
	if *statefulSet.Spec.Replicas != desiredReplicas {
		return true
	}

	// Compare containers by checking specific important fields
	if len(statefulSet.Spec.Template.Spec.Containers) != 1 {
		return true
	}

	existingContainer := statefulSet.Spec.Template.Spec.Containers[0]

	// Check image
	if existingContainer.Image != embedding.Spec.Image {
		return true
	}

	// Check args
	expectedArgs := []string{
		"--model-id", embedding.Spec.Model,
		"--port", fmt.Sprintf("%d", embedding.GetPort()),
	}
	expectedArgs = append(expectedArgs, embedding.Spec.Args...)
	if !reflect.DeepEqual(existingContainer.Args, expectedArgs) {
		return true
	}

	// Check environment variables (basic comparison of names and values)
	expectedEnvMap := make(map[string]string)
	expectedEnvMap["MODEL_ID"] = embedding.Spec.Model
	for _, env := range embedding.Spec.Env {
		expectedEnvMap[env.Name] = env.Value
	}
	if embedding.IsModelCacheEnabled() {
		expectedEnvMap["HF_HOME"] = modelCacheMountPath
	}

	existingEnvMap := make(map[string]string)
	for _, env := range existingContainer.Env {
		if env.Value != "" {
			existingEnvMap[env.Name] = env.Value
		}
	}

	if !reflect.DeepEqual(expectedEnvMap, existingEnvMap) {
		return true
	}

	// Check HF_TOKEN secret reference
	expectedHFTokenRef := embedding.Spec.HFTokenSecretRef
	var existingHFTokenRef *corev1.SecretKeySelector
	for _, env := range existingContainer.Env {
		if env.Name == "HF_TOKEN" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil {
			existingHFTokenRef = env.ValueFrom.SecretKeyRef
			break
		}
	}

	// Compare HF token secret references
	if expectedHFTokenRef != nil && existingHFTokenRef == nil {
		return true
	}
	if expectedHFTokenRef == nil && existingHFTokenRef != nil {
		return true
	}
	if expectedHFTokenRef != nil && existingHFTokenRef != nil {
		if expectedHFTokenRef.Name != existingHFTokenRef.Name || expectedHFTokenRef.Key != existingHFTokenRef.Key {
			return true
		}
	}

	// Check ports
	if len(existingContainer.Ports) != 1 || existingContainer.Ports[0].ContainerPort != embedding.GetPort() {
		return true
	}

	// Check image pull policy
	if existingContainer.ImagePullPolicy != corev1.PullPolicy(embedding.GetImagePullPolicy()) {
		return true
	}

	// Check resources
	if !reflect.DeepEqual(existingContainer.Resources, r.buildExpectedResources(embedding)) {
		return true
	}

	// Check ResourceOverrides (annotations and labels)
	if r.resourceOverridesChanged(statefulSet, embedding) {
		return true
	}

	return false
}

// buildExpectedResources builds the expected resource requirements based on the embedding spec
func (*EmbeddingServerReconciler) buildExpectedResources(embedding *mcpv1alpha1.EmbeddingServer) corev1.ResourceRequirements {
	if embedding.Spec.Resources.Limits.CPU == "" && embedding.Spec.Resources.Limits.Memory == "" &&
		embedding.Spec.Resources.Requests.CPU == "" && embedding.Spec.Resources.Requests.Memory == "" {
		return corev1.ResourceRequirements{}
	}

	resources := corev1.ResourceRequirements{
		Limits:   corev1.ResourceList{},
		Requests: corev1.ResourceList{},
	}

	if embedding.Spec.Resources.Limits.CPU != "" {
		resources.Limits[corev1.ResourceCPU] = resource.MustParse(embedding.Spec.Resources.Limits.CPU)
	}
	if embedding.Spec.Resources.Limits.Memory != "" {
		resources.Limits[corev1.ResourceMemory] = resource.MustParse(embedding.Spec.Resources.Limits.Memory)
	}
	if embedding.Spec.Resources.Requests.CPU != "" {
		resources.Requests[corev1.ResourceCPU] = resource.MustParse(embedding.Spec.Resources.Requests.CPU)
	}
	if embedding.Spec.Resources.Requests.Memory != "" {
		resources.Requests[corev1.ResourceMemory] = resource.MustParse(embedding.Spec.Resources.Requests.Memory)
	}

	return resources
}

// resourceOverridesChanged checks if ResourceOverrides have changed
func (*EmbeddingServerReconciler) resourceOverridesChanged(
	statefulSet *appsv1.StatefulSet,
	embedding *mcpv1alpha1.EmbeddingServer,
) bool {
	// Check StatefulSet annotations
	expectedAnnotations := make(map[string]string)
	expectedLabels := make(map[string]string)

	if embedding.Spec.ResourceOverrides != nil && embedding.Spec.ResourceOverrides.Deployment != nil {
		if embedding.Spec.ResourceOverrides.Deployment.Annotations != nil {
			maps.Copy(expectedAnnotations, embedding.Spec.ResourceOverrides.Deployment.Annotations)
		}
		if embedding.Spec.ResourceOverrides.Deployment.Labels != nil {
			maps.Copy(expectedLabels, embedding.Spec.ResourceOverrides.Deployment.Labels)
		}
	}

	// Check if expected annotations are present in statefulset
	for key, value := range expectedAnnotations {
		if statefulSet.Annotations[key] != value {
			return true
		}
	}

	// Check if expected labels are present in statefulset
	for key, value := range expectedLabels {
		if statefulSet.Labels[key] != value {
			return true
		}
	}

	// Check pod template annotations and labels
	expectedPodAnnotations := make(map[string]string)
	expectedPodLabels := make(map[string]string)

	if embedding.Spec.ResourceOverrides != nil &&
		embedding.Spec.ResourceOverrides.Deployment != nil &&
		embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides != nil {
		if embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Annotations != nil {
			maps.Copy(expectedPodAnnotations, embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Annotations)
		}
		if embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Labels != nil {
			maps.Copy(expectedPodLabels, embedding.Spec.ResourceOverrides.Deployment.PodTemplateMetadataOverrides.Labels)
		}
	}

	// Check if expected pod template annotations are present
	for key, value := range expectedPodAnnotations {
		if statefulSet.Spec.Template.Annotations[key] != value {
			return true
		}
	}

	// Check if expected pod template labels are present
	for key, value := range expectedPodLabels {
		if statefulSet.Spec.Template.Labels[key] != value {
			return true
		}
	}

	return false
}

// updateEmbeddingServerStatus updates the status based on statefulset state
func (r *EmbeddingServerReconciler) updateEmbeddingServerStatus(
	ctx context.Context,
	embedding *mcpv1alpha1.EmbeddingServer,
) error {
	ctxLogger := log.FromContext(ctx)

	// Set the service URL if not already set
	if embedding.Status.URL == "" {
		embedding.Status.URL = fmt.Sprintf("http://%s.%s.svc.cluster.local:%d",
			embedding.Name, embedding.Namespace, embedding.GetPort())
	}

	statefulSet := &appsv1.StatefulSet{}
	err := r.Get(ctx, types.NamespacedName{Name: embedding.Name, Namespace: embedding.Namespace}, statefulSet)
	if err != nil {
		if errors.IsNotFound(err) {
			embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhasePending
			embedding.Status.ReadyReplicas = 0
		} else {
			return err
		}
	} else {
		embedding.Status.ReadyReplicas = statefulSet.Status.ReadyReplicas
		embedding.Status.ObservedGeneration = embedding.Generation

		// Determine phase based on statefulset status
		if statefulSet.Status.ReadyReplicas > 0 {
			embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhaseRunning
			embedding.Status.Message = "Embedding server is running"
		} else if statefulSet.Status.Replicas > 0 && statefulSet.Status.ReadyReplicas == 0 {
			// Check if pods are downloading the model
			embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhaseDownloading
			embedding.Status.Message = "Downloading embedding model"
		} else {
			embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhasePending
			embedding.Status.Message = "Waiting for statefulset"
		}
	}

	err = r.Status().Update(ctx, embedding)
	if err != nil {
		ctxLogger.Error(err, "Failed to update EmbeddingServer status")
		return err
	}

	return nil
}

// finalizeEmbeddingServer performs cleanup before the EmbeddingServer is deleted
func (r *EmbeddingServerReconciler) finalizeEmbeddingServer(ctx context.Context, embedding *mcpv1alpha1.EmbeddingServer) {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Info("Finalizing EmbeddingServer", "name", embedding.Name)

	// Update status to Terminating
	embedding.Status.Phase = mcpv1alpha1.EmbeddingServerPhaseTerminating
	if err := r.Status().Update(ctx, embedding); err != nil {
		ctxLogger.Error(err, "Failed to update EmbeddingServer status to Terminating")
	}

	// Cleanup logic here if needed
	// For now, Kubernetes will handle cascade deletion of owned resources

	r.Recorder.Event(embedding, corev1.EventTypeNormal, "Deleted", "EmbeddingServer has been finalized")
}

// SetupWithManager sets up the controller with the Manager.
func (r *EmbeddingServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.EmbeddingServer{}).
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}
