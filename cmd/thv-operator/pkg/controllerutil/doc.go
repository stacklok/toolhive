// Package controllerutil provides shared utility functions for ToolHive Kubernetes controllers.
//
// This package contains helper functions extracted from the controllers package to improve
// code organization and reusability. Functions are organized by domain:
//
//   - platform.go: Platform detection and shared detector management
//   - rbac.go: RBAC (Role-Based Access Control) configuration helpers
//   - resources.go: Resource limit and request calculation utilities
//   - authz.go: Authorization (Cedar policy) configuration helpers
//   - oidc.go: OIDC (OpenID Connect) configuration helpers
//   - tokenexchange.go: Token exchange configuration for external auth
//   - config.go: General configuration merging and validation utilities
//   - podtemplatespec_builder.go: PodTemplateSpec builder for constructing pod template patches
//
// These utilities are used by multiple controllers including MCPServer, MCPRemoteProxy,
// and ToolConfig controllers to maintain consistent behavior across the operator.
package controllerutil
