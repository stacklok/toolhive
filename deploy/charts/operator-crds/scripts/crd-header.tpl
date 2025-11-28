{{- if .Values.crds.install }}
{{- if not (has "__CRD_NAME__" .Values.crds.skip) }}
# Source: {{ $.Chart.Name }}/templates/__CRD_FILENAME__
