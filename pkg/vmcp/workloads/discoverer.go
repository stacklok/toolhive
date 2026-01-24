// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package workloads provides the WorkloadDiscoverer interface for discovering
// backend workloads in both CLI and Kubernetes environments.
package workloads

import (
	"context"

	"github.com/stacklok/toolhive/pkg/vmcp"
)

// WorkloadType represents the type of workload
type WorkloadType string

const (
	// WorkloadTypeMCPServer represents an MCPServer workload
	WorkloadTypeMCPServer WorkloadType = "MCPServer"
	// WorkloadTypeMCPRemoteProxy represents an MCPRemoteProxy workload
	WorkloadTypeMCPRemoteProxy WorkloadType = "MCPRemoteProxy"
)

// TypedWorkload contains information about a discovered workload
type TypedWorkload struct {
	// Name is the name of the workload
	Name string
	// Type is the type of the workload (MCPServer or MCPRemoteProxy)
	Type WorkloadType
}

// Discoverer is the interface for workload managers used by vmcp.
// This interface contains only the methods needed for backend discovery,
// allowing both CLI and Kubernetes managers to implement it.
//
//go:generate mockgen -destination=mocks/mock_discoverer.go -package=mocks github.com/stacklok/toolhive/pkg/vmcp/workloads Discoverer
type Discoverer interface {
	// ListWorkloadsInGroup returns all workloads that belong to the specified group
	ListWorkloadsInGroup(ctx context.Context, groupName string) ([]TypedWorkload, error)

	// GetWorkloadAsVMCPBackend retrieves workload details and converts it to a vmcp.Backend.
	// The returned Backend should have all fields populated except AuthConfig,
	// which will be set by the discoverer based on the auth configuration.
	// Returns nil if the workload exists but is not accessible (e.g., no URL).
	GetWorkloadAsVMCPBackend(ctx context.Context, workload TypedWorkload) (*vmcp.Backend, error)
}
