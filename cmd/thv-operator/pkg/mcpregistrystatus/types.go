// Package mcpregistrystatus provides status management for MCPRegistry resources.
package mcpregistrystatus

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
)

// Error represents a structured error with condition information for operator components
type Error struct {
	Err             error
	Message         string
	ConditionType   string
	ConditionReason string
}

func (e *Error) Error() string {
	return e.Message
}

func (e *Error) Unwrap() error {
	return e.Err
}

//go:generate mockgen -destination=mocks/mock_status.go -package=mocks -source=types.go SyncStatusCollector,APIStatusCollector,StatusDeriver,StatusManager

// SyncStatusCollector handles sync-related status updates
type SyncStatusCollector interface {
	// Status returns the sync status
	Status() *mcpv1alpha1.SyncStatus

	// SetSyncStatus sets the detailed sync status
	SetSyncStatus(phase mcpv1alpha1.SyncPhase, message string, attemptCount int,
		lastSyncTime *metav1.Time, lastSyncHash string, serverCount int)

	// SetSyncCondition sets a sync-related condition
	SetSyncCondition(condition metav1.Condition)
}

// APIStatusCollector handles API-related status updates
type APIStatusCollector interface {
	// Status returns the API status
	Status() *mcpv1alpha1.APIStatus

	// SetAPIStatus sets the detailed API status
	SetAPIStatus(phase mcpv1alpha1.APIPhase, message string, endpoint string)

	// SetAPIReadyCondition sets the API ready condition with the specified reason, message, and status
	SetAPIReadyCondition(reason, message string, status metav1.ConditionStatus)
}

// StatusDeriver handles overall status derivation logic
type StatusDeriver interface {
	// DeriveOverallStatus derives the overall MCPRegistry phase and message from component statuses
	DeriveOverallStatus(syncStatus *mcpv1alpha1.SyncStatus, apiStatus *mcpv1alpha1.APIStatus) (mcpv1alpha1.MCPRegistryPhase, string)
}

// StatusManager orchestrates all status updates and provides access to domain-specific collectors
type StatusManager interface {
	// Sync returns the sync status collector
	Sync() SyncStatusCollector

	// API returns the API status collector
	API() APIStatusCollector

	// SetOverallStatus sets the overall phase and message explicitly (for special cases)
	SetOverallStatus(phase mcpv1alpha1.MCPRegistryPhase, message string)

	// SetCondition sets a general condition
	SetCondition(conditionType, reason, message string, status metav1.ConditionStatus)

	// UpdateStatus updates the status of the MCPRegistry if any change happened
	UpdateStatus(ctx context.Context, mcpRegistryStatus *mcpv1alpha1.MCPRegistryStatus) bool
}
