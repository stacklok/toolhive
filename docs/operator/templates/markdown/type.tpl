{{- /* Helper to render a field type with package prefixes */ -}}
{{- /* Kind values: AliasKind=0, BasicKind=1, InterfaceKind=2, MapKind=3, PointerKind=4, SliceKind=5, StructKind=6 */ -}}
{{- /* Uses markdownRenderType for basic types and imported (external) types to preserve original formatting */ -}}
{{- define "fieldType" -}}
  {{- $t := . -}}
  {{- if $t -}}
    {{- if eq $t.Kind 3 -}}
      {{- /* MapKind */ -}}
      object (keys:{{ template "fieldType" $t.KeyType }}, values:{{ template "fieldType" $t.ValueType }})
    {{- else if eq $t.Kind 5 -}}
      {{- /* SliceKind */ -}}
      {{ template "fieldType" $t.UnderlyingType }} array
    {{- else if eq $t.Kind 4 -}}
      {{- /* PointerKind - treat same as underlying */ -}}
      {{ template "fieldType" $t.UnderlyingType }}
    {{- else if or (eq $t.Kind 1) (eq $t.Kind 2) -}}
      {{- /* BasicKind or InterfaceKind - use original */ -}}
      {{ markdownRenderType $t }}
    {{- else -}}
      {{- /* StructKind=6, AliasKind=0, etc */ -}}
      {{- /* Check if type should use original rendering (external package) */ -}}
      {{- if not (hasPrefix "github.com/stacklok/toolhive" $t.Package) -}}
        {{- /* External type - use original rendering with external links */ -}}
        {{ markdownRenderTypeLink $t }}
      {{- else -}}
        {{- /* Local type - add package prefix */ -}}
        {{- $pkgParts := splitList "/" $t.Package -}}
        {{- $pkgLen := len $pkgParts -}}
        {{- $prefix := "" -}}
        {{- if ge $pkgLen 2 -}}
          {{- $prefix = printf "%s.%s" (index $pkgParts (sub $pkgLen 2)) (index $pkgParts (sub $pkgLen 1)) -}}
        {{- else -}}
          {{- $prefix = $t.Package | base -}}
        {{- end -}}
        {{- $anchor := printf "%s%s" ($prefix | replace "." "" | lower) ($t.Name | lower) -}}
        [{{ $prefix }}.{{ $t.Name }}](#{{ $anchor }})
      {{- end -}}
    {{- end -}}
  {{- end -}}
{{- end -}}

{{- define "type" -}}
{{- $type := . -}}
{{- if markdownShouldRenderType $type -}}
  {{- /* Extract last two path segments from package for disambiguation */ -}}
  {{- $pkgParts := splitList "/" $type.Package -}}
  {{- $pkgLen := len $pkgParts -}}
  {{- $prefix := "" -}}
  {{- if ge $pkgLen 2 -}}
    {{- $prefix = printf "%s.%s" (index $pkgParts (sub $pkgLen 2)) (index $pkgParts (sub $pkgLen 1)) -}}
  {{- else -}}
    {{- $prefix = $type.Package | base -}}
  {{- end -}}

#### {{ $prefix }}.{{ $type.Name }}

{{ if $type.IsAlias }}_Underlying type:_ _{{ template "fieldType" $type.UnderlyingType }}_{{ end }}

{{ $type.Doc }}

{{ if $type.Validation -}}
_Validation:_
{{- range $type.Validation }}
- {{ . }}
{{- end }}
{{- end }}

{{ if $type.References -}}
_Appears in:_
  {{- range $type.SortedReferences }}
    {{- $refPkgParts := splitList "/" .Package -}}
    {{- $refPkgLen := len $refPkgParts -}}
    {{- $refPrefix := "" -}}
    {{- if ge $refPkgLen 2 -}}
      {{- $refPrefix = printf "%s.%s" (index $refPkgParts (sub $refPkgLen 2)) (index $refPkgParts (sub $refPkgLen 1)) -}}
    {{- else -}}
      {{- $refPrefix = .Package | base -}}
    {{- end }}
- [{{ $refPrefix }}.{{ .Name }}](#{{ $refPrefix | replace "." "" | lower }}{{ .Name | lower }})
  {{- end }}
{{- end }}

{{ if $type.Members -}}
| Field | Description | Default | Validation |
| --- | --- | --- | --- |
{{ if $type.GVK -}}
| `apiVersion` _string_ | `{{ $type.GVK.Group }}/{{ $type.GVK.Version }}` | | |
| `kind` _string_ | `{{ $type.GVK.Kind }}` | | |
{{ end -}}

{{ range $type.Members -}}
| `{{ .Name  }}` _{{ template "fieldType" .Type }}_ | {{ template "type_members" . }} | {{ markdownRenderDefault .Default }} | {{ range .Validation -}} {{ markdownRenderFieldDoc . }} <br />{{ end }} |
{{ end -}}

{{ end -}}

{{ if $type.EnumValues -}} 
| Field | Description |
| --- | --- |
{{ range $type.EnumValues -}}
| `{{ .Name }}` | {{ markdownRenderFieldDoc .Doc }} |
{{ end -}}
{{ end -}}


{{- end -}}
{{- end -}}
