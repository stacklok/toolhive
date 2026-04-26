// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1beta1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1beta1"
)

// ─── EmbeddingServer ─────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=emb;embedding,categories=toolhive
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Model",type="string",JSONPath=".spec.model"
//+kubebuilder:printcolumn:name="Ready",type="integer",JSONPath=".status.readyReplicas"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EmbeddingServer is the deprecated v1alpha1 version of the EmbeddingServer resource.
type EmbeddingServer struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.EmbeddingServerSpec   `json:"spec,omitempty"`
	Status v1beta1.EmbeddingServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// EmbeddingServerList contains a list of EmbeddingServer.
type EmbeddingServerList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EmbeddingServer `json:"items"`
}

// ─── MCPExternalAuthConfig ───────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=extauth;mcpextauth,categories=toolhive
//+kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
//+kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
//+kubebuilder:printcolumn:name="References",type=string,JSONPath=`.status.referencingWorkloads`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPExternalAuthConfig is the deprecated v1alpha1 version of the MCPExternalAuthConfig resource.
type MCPExternalAuthConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPExternalAuthConfigSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPExternalAuthConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPExternalAuthConfigList contains a list of MCPExternalAuthConfig.
type MCPExternalAuthConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPExternalAuthConfig `json:"items"`
}

// ─── MCPGroup ────────────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpg;mcpgroup,categories=toolhive
//+kubebuilder:printcolumn:name="Servers",type="integer",JSONPath=".status.serverCount"
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='MCPServersChecked')].status"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPGroup is the deprecated v1alpha1 version of the MCPGroup resource.
type MCPGroup struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPGroupSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPGroupStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPGroupList contains a list of MCPGroup.
type MCPGroupList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPGroup `json:"items"`
}

// ─── MCPOIDCConfig ───────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpoidc,categories=toolhive
//+kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.type`
//+kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
//+kubebuilder:printcolumn:name="References",type=string,JSONPath=`.status.referencingWorkloads`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPOIDCConfig is the deprecated v1alpha1 version of the MCPOIDCConfig resource.
type MCPOIDCConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPOIDCConfigSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPOIDCConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPOIDCConfigList contains a list of MCPOIDCConfig.
type MCPOIDCConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPOIDCConfig `json:"items"`
}

// ─── MCPRegistry ─────────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpreg;registry,scope=Namespaced,categories=toolhive
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPRegistry is the deprecated v1alpha1 version of the MCPRegistry resource.
type MCPRegistry struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPRegistrySpec   `json:"spec,omitempty"`
	Status v1beta1.MCPRegistryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPRegistryList contains a list of MCPRegistry.
type MCPRegistryList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPRegistry `json:"items"`
}

// ─── MCPRemoteProxy ──────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=rp;mcprp,categories=toolhive
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Remote URL",type="string",JSONPath=".spec.remoteUrl"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPRemoteProxy is the deprecated v1alpha1 version of the MCPRemoteProxy resource.
type MCPRemoteProxy struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPRemoteProxySpec   `json:"spec,omitempty"`
	Status v1beta1.MCPRemoteProxyStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPRemoteProxyList contains a list of MCPRemoteProxy.
type MCPRemoteProxyList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPRemoteProxy `json:"items"`
}

// ─── MCPServer ───────────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpserver;mcpservers,categories=toolhive
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
//+kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".status.readyReplicas"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServer is the deprecated v1alpha1 version of the MCPServer resource.
type MCPServer struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPServerSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPServerList contains a list of MCPServer.
type MCPServerList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServer `json:"items"`
}

// ─── MCPServerEntry ──────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpentry,categories=toolhive
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
//+kubebuilder:printcolumn:name="Transport",type="string",JSONPath=".spec.transport"
//+kubebuilder:printcolumn:name="Remote URL",type="string",JSONPath=".spec.remoteUrl"
//+kubebuilder:printcolumn:name="Group",type="string",JSONPath=".spec.groupRef.name"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// MCPServerEntry is the deprecated v1alpha1 version of the MCPServerEntry resource.
type MCPServerEntry struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPServerEntrySpec   `json:"spec,omitempty"`
	Status v1beta1.MCPServerEntryStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPServerEntryList contains a list of MCPServerEntry.
type MCPServerEntryList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPServerEntry `json:"items"`
}

// ─── MCPTelemetryConfig ──────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mcpotel,categories=toolhive
//+kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.openTelemetry.endpoint`
//+kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
//+kubebuilder:printcolumn:name="Tracing",type=boolean,JSONPath=`.spec.openTelemetry.tracing.enabled`
//+kubebuilder:printcolumn:name="Metrics",type=boolean,JSONPath=`.spec.openTelemetry.metrics.enabled`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPTelemetryConfig is the deprecated v1alpha1 version of the MCPTelemetryConfig resource.
type MCPTelemetryConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPTelemetryConfigSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPTelemetryConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPTelemetryConfigList contains a list of MCPTelemetryConfig.
type MCPTelemetryConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPTelemetryConfig `json:"items"`
}

// ─── MCPWebhookConfig ────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=mwc,categories=toolhive
//+kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
//+kubebuilder:printcolumn:name="References",type=string,JSONPath=`.status.referencingWorkloads`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPWebhookConfig is the deprecated v1alpha1 version of the MCPWebhookConfig resource.
type MCPWebhookConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPWebhookConfigSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPWebhookConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPWebhookConfigList contains a list of MCPWebhookConfig.
type MCPWebhookConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPWebhookConfig `json:"items"`
}

// ─── MCPToolConfig ───────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=tc;toolconfig,categories=toolhive
//+kubebuilder:printcolumn:name="Valid",type=string,JSONPath=`.status.conditions[?(@.type=='Valid')].status`
//+kubebuilder:printcolumn:name="References",type=string,JSONPath=`.status.referencingWorkloads`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MCPToolConfig is the deprecated v1alpha1 version of the MCPToolConfig resource.
type MCPToolConfig struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.MCPToolConfigSpec   `json:"spec,omitempty"`
	Status v1beta1.MCPToolConfigStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// MCPToolConfigList contains a list of MCPToolConfig.
type MCPToolConfigList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MCPToolConfig `json:"items"`
}

// ─── VirtualMCPCompositeToolDefinition ───────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vmcpctd;compositetool,categories=toolhive
//+kubebuilder:printcolumn:name="Workflow",type="string",JSONPath=".spec.name",description="Workflow name"
//+kubebuilder:printcolumn:name="Steps",type="integer",JSONPath=".spec.steps[*]",description="Number of steps"
//+kubebuilder:printcolumn:name="Status",type="string",JSONPath=".status.validationStatus",description="Validation status"
//+kubebuilder:printcolumn:name="Refs",type="integer",JSONPath=".status.referencingVirtualServers[*]",description="Refs"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"

// VirtualMCPCompositeToolDefinition is the deprecated v1alpha1 version of the VirtualMCPCompositeToolDefinition resource.
type VirtualMCPCompositeToolDefinition struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.VirtualMCPCompositeToolDefinitionSpec   `json:"spec,omitempty"`
	Status v1beta1.VirtualMCPCompositeToolDefinitionStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMCPCompositeToolDefinitionList contains a list of VirtualMCPCompositeToolDefinition.
type VirtualMCPCompositeToolDefinitionList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMCPCompositeToolDefinition `json:"items"`
}

// ─── VirtualMCPServer ────────────────────────────────────────────────────────

//+kubebuilder:object:root=true
//+kubebuilder:deprecatedversion:warning="toolhive.stacklok.dev/v1alpha1 is deprecated; use v1beta1"
//+kubebuilder:subresource:status
//+kubebuilder:resource:shortName=vmcp;virtualmcp,categories=toolhive
//+kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="The phase of the VirtualMCPServer"
//+kubebuilder:printcolumn:name="URL",type="string",JSONPath=".status.url",description="Virtual MCP server URL"
//+kubebuilder:printcolumn:name="Backends",type="integer",JSONPath=".status.backendCount",description="Discovered backends count"
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Age"
//+kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"

// VirtualMCPServer is the deprecated v1alpha1 version of the VirtualMCPServer resource.
type VirtualMCPServer struct {
	metav1.TypeMeta   `json:",inline"` // nolint:revive
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   v1beta1.VirtualMCPServerSpec   `json:"spec,omitempty"`
	Status v1beta1.VirtualMCPServerStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VirtualMCPServerList contains a list of VirtualMCPServer.
type VirtualMCPServerList struct {
	metav1.TypeMeta `json:",inline"` // nolint:revive
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VirtualMCPServer `json:"items"`
}

// ─── Scheme Registration ─────────────────────────────────────────────────────

func init() {
	SchemeBuilder.Register(
		&EmbeddingServer{}, &EmbeddingServerList{},
		&MCPExternalAuthConfig{}, &MCPExternalAuthConfigList{},
		&MCPGroup{}, &MCPGroupList{},
		&MCPOIDCConfig{}, &MCPOIDCConfigList{},
		&MCPRegistry{}, &MCPRegistryList{},
		&MCPRemoteProxy{}, &MCPRemoteProxyList{},
		&MCPServer{}, &MCPServerList{},
		&MCPServerEntry{}, &MCPServerEntryList{},
		&MCPTelemetryConfig{}, &MCPTelemetryConfigList{},
		&MCPWebhookConfig{}, &MCPWebhookConfigList{},
		&MCPToolConfig{}, &MCPToolConfigList{},
		&VirtualMCPCompositeToolDefinition{}, &VirtualMCPCompositeToolDefinitionList{},
		&VirtualMCPServer{}, &VirtualMCPServerList{},
	)
}
