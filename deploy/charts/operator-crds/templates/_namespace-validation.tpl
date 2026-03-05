{{- if ne .Release.Namespace "toolhive-system" }}
{{- fail "The toolhive-operator-crds chart must be installed in the toolhive-system namespace. Use: helm install <release-name> -n toolhive-system --create-namespace" }}
{{- end }}
