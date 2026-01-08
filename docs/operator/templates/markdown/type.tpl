{{- define "type" -}}
{{- $type := . -}}
{{- if markdownShouldRenderType $type -}}
{{- /* Render types with +gendoc marker OR in api/v1alpha1 package */ -}}
{{- $hasGendoc := index $type.Markers "gendoc" -}}
{{- $isV1alpha1 := regexMatch "v1alpha1" $type.Package -}}
{{- if or $hasGendoc $isV1alpha1 -}}

#### {{ base $type.Package }}.{{ $type.Name }}

{{ if $type.IsAlias }}_Underlying type:_ _{{ base $type.UnderlyingType.Package }}.{{ markdownRenderTypeLink $type.UnderlyingType  }}_{{ end }}

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
- [{{ base .Package }}.{{ .Name }}](#{{ base .Package | lower }}{{ .Name | lower }})
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
| `{{ .Name  }}` _{{ markdownRenderType .Type }}_ | {{ template "type_members" . }} | {{ markdownRenderDefault .Default }} | {{ range .Validation -}} {{ markdownRenderFieldDoc . }} <br />{{ end }} |
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
{{- end -}}
