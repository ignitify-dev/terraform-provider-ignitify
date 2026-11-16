Third-party notices for terraform-provider-ignitify

The provider binary is built with the Go modules below. Each is listed with
its licence, the URL of its licence file and the licence text. The source of
each module is at the version in go.mod and go.sum.
{{ range . }}
================================================================================
{{ .Name }} {{ .Version }}
Licence: {{ .LicenseName }}
{{ .LicenseURL }}
--------------------------------------------------------------------------------
{{ .LicenseText }}
{{ end }}
