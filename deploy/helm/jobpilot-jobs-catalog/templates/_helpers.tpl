{{- define "jobpilot-jobs-catalog.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "jobpilot-jobs-catalog.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "jobpilot-jobs-catalog.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
