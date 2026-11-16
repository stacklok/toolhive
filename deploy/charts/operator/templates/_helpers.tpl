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

{{- define "toolhive.operator.managerRules" -}}
- apiGroups:
  - ""
  resources:
  - configmaps
  - persistentvolumeclaims
  - secrets
  - serviceaccounts
  - services
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - ""
  resources:
  - pods
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - ""
  resources:
  - pods/attach
  verbs:
  - create
  - get
- apiGroups:
  - ""
  resources:
  - pods/log
  verbs:
  - get
- apiGroups:
  - apps
  resources:
  - deployments
  - statefulsets
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - coordination.k8s.io
  resources:
  - leases
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - events.k8s.io
  resources:
  - events
  verbs:
  - create
  - patch
- apiGroups:
  - gateway.networking.k8s.io
  resources:
  - gateways
  - httproutes
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - rbac.authorization.k8s.io
  resources:
  - rolebindings
  - roles
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - toolhive.stacklok.dev
  resources:
  - '*'
  verbs:
  - get
  - list
  - update
- apiGroups:
  - toolhive.stacklok.dev
  resources:
  - embeddingservers
  - mcpauthzconfigs
  - mcpexternalauthconfigs
  - mcpgroups
  - mcpoidcconfigs
  - mcpregistries
  - mcpremoteproxies
  - mcpservers
  - mcptoolconfigs
  - mcpwebhookconfigs
  - virtualmcpservers
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - toolhive.stacklok.dev
  resources:
  - embeddingservers/finalizers
  - mcpauthzconfigs/finalizers
  - mcpexternalauthconfigs/finalizers
  - mcpgroups/finalizers
  - mcpoidcconfigs/finalizers
  - mcpregistries/finalizers
  - mcpservers/finalizers
  - mcptelemetryconfigs/finalizers
  - mcptoolconfigs/finalizers
  - mcpwebhookconfigs/finalizers
  verbs:
  - update
- apiGroups:
  - toolhive.stacklok.dev
  resources:
  - embeddingservers/status
  - mcpauthzconfigs/status
  - mcpexternalauthconfigs/status
  - mcpgroups/status
  - mcpoidcconfigs/status
  - mcpregistries/status
  - mcpremoteproxies/status
  - mcpserverentries/status
  - mcpservers/status
  - mcptelemetryconfigs/status
  - mcptoolconfigs/status
  - mcpwebhookconfigs/status
  - virtualmcpservers/status
  verbs:
  - get
  - patch
  - update
- apiGroups:
  - toolhive.stacklok.dev
  resources:
  - mcpserverentries
  - virtualmcpcompositetooldefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - toolhive.stacklok.dev
  resources:
  - mcptelemetryconfigs
  verbs:
  - get
  - list
  - patch
  - update
  - watch
{{- end -}}

{{- define "toolhive.operator.crdRules" -}}
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - apiextensions.k8s.io
  resources:
  - customresourcedefinitions/status
  verbs:
  - patch
  - update
{{- end -}}
