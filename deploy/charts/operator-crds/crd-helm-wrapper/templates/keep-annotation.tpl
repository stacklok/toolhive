    {{- if .Values.crds.keep }}
    helm.sh/resource-policy: keep
    {{- end }}
