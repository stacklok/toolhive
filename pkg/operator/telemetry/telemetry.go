// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package telemetry provides telemetry functionality for the ToolHive operator.
package telemetry

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/versions"
)

const (
	// updateInterval defines how often to check for updates
	updateInterval = 30 * time.Minute
	// configMapName is the name of the ConfigMap used to store telemetry data
	configMapName = "toolhive-operator-telemetry"
	// configMapNamespace is the namespace where the ConfigMap is stored
	configMapNamespace = "toolhive-system"
	// instanceIDKey is the key used to store the instance ID in the ConfigMap
	instanceIDKey = "instance-id"
)

// Service handles telemetry operations for the operator
type Service struct {
	client        client.Client
	versionClient updates.VersionClient
	namespace     string
	// instanceID is the ID EnsureInstanceID resolved at startup, before the
	// manager runs the telemetry worker. Workloads carry it, so the worker
	// stores it again if the ConfigMap loses it.
	instanceID string
}

// LeaderTelemetryRunnable runs telemetry checks only when this instance is the leader
type LeaderTelemetryRunnable struct {
	TelemetryService *Service
}

// Start starts the telemetry runner
func (t *LeaderTelemetryRunnable) Start(ctx context.Context) error {
	ctxLogger := log.FromContext(ctx)
	ctxLogger.Info("Leader elected, starting telemetry worker")

	// Start telemetry worker in a goroutine with the leader context
	// When leadership is lost, ctx will be cancelled and telemetry will stop
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ctxLogger.Error(fmt.Errorf("telemetry worker panic: %v", r), "Telemetry worker panicked")
			}
		}()
		t.TelemetryService.StartTelemetryWorker(ctx)
	}()

	// Wait for context cancellation (leadership lost or shutdown)
	<-ctx.Done()
	ctxLogger.Info("Leadership lost, telemetry worker stopped")
	return nil
}

// NeedsLeaderElection indicates whether this runnable needs leader election
func (*LeaderTelemetryRunnable) NeedsLeaderElection() bool {
	// This runnable should only run when this instance is the leader
	return true
}

// telemetryData represents the structure of telemetry data stored in ConfigMap
type telemetryData struct {
	InstanceID      string    `json:"instance_id"`
	LastUpdateCheck time.Time `json:"last_update_check"`
	LatestVersion   string    `json:"latest_version"`
}

// NewService creates a new Service instance
func NewService(k8sClient client.Client, namespace string) *Service {
	if namespace == "" {
		namespace = configMapNamespace
	}
	return &Service{
		client:        k8sClient,
		versionClient: updates.NewVersionClientForComponent("operator", "", false),
		namespace:     namespace,
	}
}

// InstanceIDBackoff is how EnsureInstanceID retries at operator startup: five
// attempts over about 15 seconds.
var InstanceIDBackoff = wait.Backoff{Steps: 5, Duration: time.Second, Factor: 2, Jitter: 0.1}

// instanceIDTimeout bounds the whole startup lookup, including a request that
// stalls, so the operator reaches its health endpoints well within the default
// liveness budget.
const instanceIDTimeout = 30 * time.Second

// ErrInstanceIDUnavailable is returned by EnsureInstanceID when the ID could
// not be read or stored because of errors that may be transient (the API
// server is unreachable, overloaded or timing out). The caller should exit and
// let the pod restart rather than run without the ID: workload pod templates
// carry it, so a replica running without it would roll every workload when it
// becomes the leader, and roll them back when leadership moves again.
var ErrInstanceIDUnavailable = stderrors.New("telemetry instance ID unavailable")

// EnsureInstanceID returns this operator installation's instance ID, creating
// and storing it in the telemetry ConfigMap if it does not exist yet, or if the
// ConfigMap holds no usable ID. It is meant to run once at startup, before the
// manager's cache starts, so it reads through the given uncached reader.
//
// Errors that may be transient are retried with the given backoff, within an
// overall deadline; if they persist, the error wraps ErrInstanceIDUnavailable.
// Errors that a retry cannot fix (forbidden, unauthorized, invalid) are
// returned at once: every replica gets the same error, so they all run without
// the ID and agree. Concurrent replicas may race to write the ID; the loser
// reads the winner's ID. Returns an empty ID when update checks are disabled,
// without touching the ConfigMap.
func (s *Service) EnsureInstanceID(ctx context.Context, reader client.Reader, backoff wait.Backoff) (string, error) {
	if updates.ShouldSkipUpdateChecks() {
		return "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, instanceIDTimeout)
	defer cancel()

	var id string
	var lastErr error
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(ctx context.Context) (bool, error) {
		id, lastErr = s.ensureInstanceIDOnce(ctx, reader)
		switch {
		case lastErr == nil:
			return true, nil
		case isPermanentAPIError(lastErr):
			return false, lastErr
		default:
			return false, nil
		}
	})
	switch {
	case err == nil:
		s.instanceID = id
		return id, nil
	case isPermanentAPIError(err):
		return "", err
	case lastErr != nil:
		return "", fmt.Errorf("%w: %w", ErrInstanceIDUnavailable, lastErr)
	default:
		return "", fmt.Errorf("%w: %w", ErrInstanceIDUnavailable, err)
	}
}

// isPermanentAPIError reports whether retrying the same request cannot help.
func isPermanentAPIError(err error) bool {
	return errors.IsForbidden(err) || errors.IsUnauthorized(err) || errors.IsInvalid(err)
}

// ensureInstanceIDOnce makes one attempt at reading or storing the instance ID.
func (s *Service) ensureInstanceIDOnce(ctx context.Context, reader client.Reader) (string, error) {
	key := types.NamespacedName{Name: configMapName, Namespace: s.namespace}
	cm, id, err := readInstanceID(ctx, reader, key)
	if err != nil || id != "" {
		return id, err
	}

	data := &telemetryData{InstanceID: uuid.NewString()}
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal telemetry data: %w", err)
	}

	if cm == nil {
		cm = &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: configMapName, Namespace: s.namespace},
			Data:       map[string]string{instanceIDKey: string(dataBytes)},
		}
		err = s.client.Create(ctx, cm)
		switch {
		case err == nil:
			return data.InstanceID, nil
		case errors.IsAlreadyExists(err):
			return readStoredInstanceID(ctx, reader, key)
		default:
			return "", fmt.Errorf("failed to create telemetry ConfigMap: %w", err)
		}
	}

	// The ConfigMap exists without a usable ID. Store one now; otherwise the
	// leader's update check would generate a different one later, and the next
	// operator restart would roll every workload to pick it up.
	cm = cm.DeepCopy()
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	cm.Data[instanceIDKey] = string(dataBytes)
	err = s.client.Update(ctx, cm)
	switch {
	case err == nil:
		return data.InstanceID, nil
	case errors.IsConflict(err):
		return readStoredInstanceID(ctx, reader, key)
	default:
		return "", fmt.Errorf("failed to update telemetry ConfigMap: %w", err)
	}
}

// CheckForUpdates checks for updates and sends telemetry data
func (s *Service) CheckForUpdates(ctx context.Context) error {
	if updates.ShouldSkipUpdateChecks() {
		return nil
	}

	logger := log.FromContext(ctx)

	// Get or create telemetry data
	data, err := s.getTelemetryData(ctx)
	if err != nil {
		return fmt.Errorf("failed to get telemetry data: %w", err)
	}

	// Check if we need to make an API request based on last update time
	if time.Since(data.LastUpdateCheck) < updateInterval {
		// Too soon, skip the check
		logger.V(1).Info("Skipping update check, too soon since last check",
			"lastCheck", data.LastUpdateCheck,
			"interval", updateInterval)
		return nil
	}

	logger.Info("Checking for updates...")

	// Get the latest version from the API
	currentVersion := versions.GetVersionInfo().Version
	latestVersion, err := s.versionClient.GetLatestVersion(data.InstanceID, currentVersion)
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	// Update telemetry data
	data.LastUpdateCheck = time.Now()
	data.LatestVersion = latestVersion

	// Save updated telemetry data
	if err := s.saveTelemetryData(ctx, data); err != nil {
		return fmt.Errorf("failed to save telemetry data: %w", err)
	}

	logger.Info("Update check completed",
		"currentVersion", currentVersion,
		"latestVersion", latestVersion)

	return nil
}

// getTelemetryData retrieves telemetry data from the ConfigMap. If the
// ConfigMap is missing or holds no usable ID, the data starts over (forcing an
// immediate check) with the ID the workloads already carry, or, if startup did
// not resolve one, an ID stored with the same race handling as at startup.
func (s *Service) getTelemetryData(ctx context.Context) (*telemetryData, error) {
	key := types.NamespacedName{Name: configMapName, Namespace: s.namespace}
	cm := &corev1.ConfigMap{}
	if err := s.client.Get(ctx, key, cm); err != nil && !errors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	data := &telemetryData{}
	if err := json.Unmarshal([]byte(cm.Data[instanceIDKey]), data); err == nil && data.InstanceID != "" {
		return data, nil
	}

	id := s.instanceID
	if id == "" {
		var err error
		if id, err = s.ensureInstanceIDOnce(ctx, s.client); err != nil {
			return nil, err
		}
	}
	return &telemetryData{InstanceID: id}, nil
}

// saveTelemetryData saves telemetry data to ConfigMap
func (s *Service) saveTelemetryData(ctx context.Context, data *telemetryData) error {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal telemetry data: %w", err)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: s.namespace,
		},
		Data: map[string]string{
			instanceIDKey: string(dataBytes),
		},
	}

	// Try to get existing ConfigMap first
	existingCM := &corev1.ConfigMap{}
	err = s.client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: s.namespace,
	}, existingCM)

	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist, create it
			return s.client.Create(ctx, cm)
		}
		return fmt.Errorf("failed to get existing ConfigMap: %w", err)
	}

	// ConfigMap exists, update it. An ID already stored there wins: workloads
	// carry it, so it must never be replaced.
	stored := &telemetryData{}
	if err := json.Unmarshal([]byte(existingCM.Data[instanceIDKey]), stored); err == nil &&
		stored.InstanceID != "" && stored.InstanceID != data.InstanceID {
		updated := *data
		updated.InstanceID = stored.InstanceID
		if dataBytes, err = json.Marshal(&updated); err != nil {
			return fmt.Errorf("failed to marshal telemetry data: %w", err)
		}
		cm.Data[instanceIDKey] = string(dataBytes)
	}
	existingCM.Data = cm.Data
	return s.client.Update(ctx, existingCM)
}

// StartTelemetryWorker starts a background worker that periodically checks for updates
// This should only be called by the leader
func (s *Service) StartTelemetryWorker(ctx context.Context) {
	logger := log.FromContext(ctx)
	logger.Info("Starting telemetry worker")

	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	// Run initial check
	if err := s.CheckForUpdates(ctx); err != nil {
		logger.Error(err, "Failed initial telemetry check")
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("Telemetry worker stopped")
			return
		case <-ticker.C:
			if err := s.CheckForUpdates(ctx); err != nil {
				logger.Error(err, "Failed telemetry check")
			}
		}
	}
}

// readInstanceID returns the telemetry ConfigMap and the instance ID stored in
// it. The ConfigMap is nil if it does not exist; the ID is empty if the
// ConfigMap holds no usable one.
func readInstanceID(
	ctx context.Context, reader client.Reader, key types.NamespacedName,
) (*corev1.ConfigMap, string, error) {
	cm := &corev1.ConfigMap{}
	if err := reader.Get(ctx, key, cm); err != nil {
		if errors.IsNotFound(err) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("failed to get telemetry ConfigMap: %w", err)
	}
	data := &telemetryData{}
	if err := json.Unmarshal([]byte(cm.Data[instanceIDKey]), data); err != nil {
		return cm, "", nil
	}
	return cm, data.InstanceID, nil
}

// readStoredInstanceID reads back the ID another replica stored after losing a
// race to write it. A missing ID is an error, so the caller retries.
func readStoredInstanceID(ctx context.Context, reader client.Reader, key types.NamespacedName) (string, error) {
	_, id, err := readInstanceID(ctx, reader, key)
	if err == nil && id == "" {
		return "", fmt.Errorf("telemetry ConfigMap has no instance ID after a concurrent write")
	}
	return id, err
}
