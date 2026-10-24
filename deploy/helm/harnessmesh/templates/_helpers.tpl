{{- define "harnessmesh.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "harnessmesh.fullname" -}}
{{- printf "%s-%s" .Release.Name (include "harnessmesh.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- define "harnessmesh.labels" -}}
app.kubernetes.io/name: {{ include "harnessmesh.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
