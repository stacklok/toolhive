{{- if .Values.crds.install }}
{{- if not (has "__CRD_NAME__" .Values.crds.skip) }}
