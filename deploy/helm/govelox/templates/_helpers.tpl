{{/* Chart short name (overridable). */}}
{{- define "govelox.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* Release-scoped base name. With fullnameOverride=velox → "velox". */}}
{{- define "govelox.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/* Per-component names. */}}
{{- define "govelox.engine.fullname" -}}{{ printf "%s-engine" (include "govelox.fullname" .) }}{{- end -}}
{{- define "govelox.membership.fullname" -}}{{ printf "%s-membership" (include "govelox.fullname" .) }}{{- end -}}
{{- define "govelox.gateway.fullname" -}}{{ printf "%s-gateway" (include "govelox.fullname" .) }}{{- end -}}
{{- define "govelox.config.fullname" -}}{{ printf "%s-config" (include "govelox.fullname" .) }}{{- end -}}

{{/* Fully-qualified DNS of the engine headless Service (used for gossip seeds). */}}
{{- define "govelox.engine.fqdn" -}}
{{- printf "%s.%s.svc.cluster.local" (include "govelox.engine.fullname" .) .Release.Namespace -}}
{{- end -}}

{{/* Common metadata labels. */}}
{{- define "govelox.labels" -}}
app.kubernetes.io/name: {{ include "govelox.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}
