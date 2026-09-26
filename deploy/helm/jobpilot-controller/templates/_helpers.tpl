{{- define "jobpilot-controller.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "jobpilot-controller.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name (include "jobpilot-controller.name" .) | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{- define "jobpilot-controller.labels" -}}
app.kubernetes.io/name: {{ include "jobpilot-controller.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}

{{- define "jobpilot-controller.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "jobpilot-controller.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- required "serviceAccount.name is required when serviceAccount.create=false" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{- define "jobpilot-controller.webhookSecretName" -}}
{{- default (printf "%s-webhook-tls" (include "jobpilot-controller.fullname" .)) .Values.webhook.tlsSecretName }}
{{- end }}
