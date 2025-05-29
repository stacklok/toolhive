// Package telemetry provides telemetry functionality for the ToolHive operator.
package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/stacklok/toolhive/pkg/updates"
	"github.com/stacklok/toolhive/pkg/versions"
)

const (
	// updateInterval defines how often to check for updates
	updateInterval = 4 * time.Hour
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
		versionClient: updates.NewVersionClientWithSuffix("operator"),
		namespace:     namespace,
	}
}

// CheckForUpdates checks for updates and sends telemetry data
func (s *Service) CheckForUpdates(ctx context.Context) error {
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
		logger.Error(err, "Failed to check for updates")
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

// getTelemetryData retrieves telemetry data from ConfigMap or creates new data
func (s *Service) getTelemetryData(ctx context.Context) (*telemetryData, error) {
	cm := &corev1.ConfigMap{}
	err := s.client.Get(ctx, types.NamespacedName{
		Name:      configMapName,
		Namespace: s.namespace,
	}, cm)

	if err != nil {
		if errors.IsNotFound(err) {
			// ConfigMap doesn't exist, create new telemetry data
			return &telemetryData{
				InstanceID:      uuid.NewString(),
				LastUpdateCheck: time.Time{}, // Zero time to force immediate check
				LatestVersion:   "",
			}, nil
		}
		return nil, fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// Parse existing data
	data := &telemetryData{}
	if rawData, exists := cm.Data[instanceIDKey]; exists {
		if err := json.Unmarshal([]byte(rawData), data); err != nil {
			// If we can't parse the data, create new data
			return &telemetryData{
				InstanceID:      uuid.NewString(),
				LastUpdateCheck: time.Time{},
				LatestVersion:   "",
			}, nil
		}
	} else {
		// No data in ConfigMap, create new
		return &telemetryData{
			InstanceID:      uuid.NewString(),
			LastUpdateCheck: time.Time{},
			LatestVersion:   "",
		}, nil
	}

	return data, nil
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

	// ConfigMap exists, update it
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
