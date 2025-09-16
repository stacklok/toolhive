// Package sync provides synchronization management interfaces and implementations
// for MCPRegistry resources in the ToolHive operator.
//
// This package implements a clean separation of concerns by extracting all
// sync-related logic from the controller into dedicated interfaces:
//
//   - Manager: Main interface for orchestrating sync operations
//   - DataChangeDetector: Detects changes in source data using hash comparison
//   - ManualSyncChecker: Handles annotation-based manual sync triggers
//   - AutomaticSyncChecker: Manages time-based automatic sync scheduling
//
// The package follows Go best practices by using interfaces for testability
// and dependency injection, while providing concrete struct implementations
// for actual functionality.
//
// Key features:
//   - Hash-based change detection to avoid unnecessary syncs
//   - Manual sync support via annotations
//   - Automatic sync scheduling with configurable intervals
//   - Comprehensive error handling and status management
//   - Clean integration with Kubernetes controller patterns
package sync
