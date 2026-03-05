    meta.helm.sh/release-namespace: toolhive-system
    {{- if .Values.crds.keep }}
    helm.sh/resource-policy: keep
    {{- end }}
