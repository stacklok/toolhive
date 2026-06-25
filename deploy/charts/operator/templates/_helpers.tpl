{{/*
Expand the name of the chart.
*/}}
{{- define "toolhive-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "toolhive-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "toolhive-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "toolhive-operator.labels" -}}
helm.sh/chart: {{ include "toolhive-operator.chart" . }}
{{ include "toolhive-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "toolhive-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "toolhive-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/part-of: {{ include "toolhive-operator.name" . }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "toolhive-operator.serviceAccountName" -}}
{{- if .Values.operator.serviceAccount.create }}
{{- default (include "toolhive-operator.fullname" .) .Values.operator.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.operator.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Common labels for the toolhive resources
*/}}
{{- define "toolhive.labels" -}}
app: toolhive
app.kubernetes.io/name: toolhive
{{- end }}

{{/*
Validate feature-flag / RBAC-scope combinations and fail the render early with
an actionable message rather than deploying a wedged operator.

The StorageVersionMigrator controller watches cluster-scoped
CustomResourceDefinition objects and re-stores custom resources across every
namespace, so it requires cluster-scoped RBAC and a cluster-scoped manager
cache. A namespace-scoped operator (operator.rbac.scope=namespace) gets only
per-namespace RoleBindings and a namespace-restricted cache, so the controller
cannot sync its CRD informer and would prevent the manager from starting. We
fail loudly here instead of silently dropping the feature, because a silent
drop would let a namespace-scoped admin believe storedVersions are being kept
clean when they are not.
*/}}
{{- define "toolhive-operator.validateStorageVersionMigrator" -}}
{{- if and .Values.operator.features.storageVersionMigrator (ne .Values.operator.rbac.scope "cluster") -}}
{{- fail "operator.features.storageVersionMigrator requires operator.rbac.scope=cluster: the StorageVersionMigrator controller watches cluster-scoped CustomResourceDefinitions and re-stores resources across all namespaces, which a namespace-scoped operator cannot do. Set operator.features.storageVersionMigrator=false for namespace-scoped installs." -}}
{{- end -}}
{{- end -}}