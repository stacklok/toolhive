{{- define "gvDetails" -}}
{{- $gv := . -}}

## {{ $gv.GroupVersionString }}

{{- if $gv.Kinds  }}
### Resource Types
{{- range $gv.SortedKinds }}
  {{- $type := $gv.TypeForKind . -}}
  {{- $pkgParts := splitList "/" $type.Package -}}
  {{- $pkgLen := len $pkgParts -}}
  {{- $prefix := "" -}}
  {{- if ge $pkgLen 2 -}}
    {{- $prefix = printf "%s.%s" (index $pkgParts (sub $pkgLen 2)) (index $pkgParts (sub $pkgLen 1)) -}}
  {{- else -}}
    {{- $prefix = $type.Package | base -}}
  {{- end }}
- [{{ $prefix }}.{{ $type.Name }}](#{{ $prefix | replace "." "" | lower }}{{ $type.Name | lower }})
{{- end }}
{{ end }}

{{ range $gv.SortedTypes }}
{{ template "type" . }}
{{ end }}

{{- end -}}
